// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// faketime_go_exec bridges libfaketime (LD_PRELOAD) to unmodified Linux x86_64
// Go binaries (such as `grain daemon`). Go's runtime bypasses libc and reads
// wall time via the kernel vDSO (AT_SYSINFO_EHDR) or raw SYSCALL 228
// (SYS_clock_gettime). This wrapper:
//   1. Strips AT_SYSINFO_EHDR from the ELF auxiliary vector on execve so Go
//      falls back from vDSO to SYS_clock_gettime.
//   2. Installs a narrow BPF seccomp filter returning SECCOMP_RET_TRACE ONLY
//      for SYS_clock_gettime(CLOCK_REALTIME / CLOCK_REALTIME_COARSE),
//      SYS_gettimeofday, and SYS_time — leaving CLOCK_MONOTONIC and all other
//      syscalls on the zero-overhead native kernel fast path.
//   3. Answers traced wall-clock syscalls using libc's clock_gettime(), which
//      libfaketime intercepts cleanly.

#define _GNU_SOURCE
#include <elf.h>
#include <errno.h>
#include <linux/audit.h>
#include <linux/filter.h>
#include <linux/seccomp.h>
#include <signal.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <sys/ptrace.h>
#include <sys/syscall.h>
#include <sys/time.h>
#include <sys/uio.h>
#include <sys/user.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

static volatile pid_t g_root_pid = 0;

static void forward_sig(int sig) {
  if (g_root_pid > 0) {
    kill(g_root_pid, sig);
  }
}

static void strip_vdso_auxv(pid_t pid) {
  struct user_regs_struct regs;
  if (ptrace(PTRACE_GETREGS, pid, NULL, &regs) < 0) {
    return;
  }
  uintptr_t sp = (uintptr_t)regs.rsp;
  long argc = ptrace(PTRACE_PEEKDATA, pid, (void *)sp, NULL);
  if (argc < 0 || argc > 131072) {
    return;
  }
  // Skip argc + argv[0..argc-1] + NULL
  sp += (uintptr_t)(argc + 2) * sizeof(uintptr_t);
  // Skip envp[] up to NULL
  for (int i = 0; i < 131072; i++) {
    errno = 0;
    long v = ptrace(PTRACE_PEEKDATA, pid, (void *)sp, NULL);
    if (errno != 0) {
      return;
    }
    sp += sizeof(uintptr_t);
    if (v == 0) {
      break;
    }
  }
  // Walk Elf64_auxv_t pairs (a_type, a_val) until AT_NULL (0)
  for (int i = 0; i < 1024; i++) {
    errno = 0;
    long a_type = ptrace(PTRACE_PEEKDATA, pid, (void *)sp, NULL);
    if (errno != 0 || a_type == AT_NULL) {
      break;
    }
    if (a_type == AT_SYSINFO_EHDR || a_type == AT_SYSINFO) {
      ptrace(PTRACE_POKEDATA, pid, (void *)sp, (void *)(uintptr_t)AT_IGNORE);
    }
    sp += 2 * sizeof(uintptr_t);
  }
}

static int install_realtime_seccomp(void) {
  struct sock_filter filter[] = {
      // Verify x86_64 architecture
      BPF_STMT(BPF_LD | BPF_W | BPF_ABS, offsetof(struct seccomp_data, arch)),
      BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, AUDIT_ARCH_X86_64, 1, 0),
      BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),

      // Load syscall number
      BPF_STMT(BPF_LD | BPF_W | BPF_ABS, offsetof(struct seccomp_data, nr)),
      BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, __NR_clock_gettime, 3, 0),
      BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, __NR_gettimeofday, 6, 0),
      BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, __NR_time, 5, 0),
      BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),

      // __NR_clock_gettime: check clockid_t (low 32 bits of args[0])
      BPF_STMT(BPF_LD | BPF_W | BPF_ABS, offsetof(struct seccomp_data, args[0])),
      BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, CLOCK_REALTIME, 2, 0),
      BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, CLOCK_REALTIME_COARSE, 1, 0),
      BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),

      BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_TRACE),
  };
  struct sock_fprog prog = {
      .len = (unsigned short)(sizeof(filter) / sizeof(filter[0])),
      .filter = filter,
  };
  if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) < 0) {
    return -1;
  }
  return prctl(PR_SET_SECCOMP, SECCOMP_MODE_FILTER, &prog);
}

struct pending_exit {
  pid_t pid;
  long nr;
  uintptr_t arg0;
  uintptr_t arg1;
};

#define MAX_PENDING 512
static struct pending_exit g_pending[MAX_PENDING];

static void set_pending(pid_t pid, long nr, uintptr_t a0, uintptr_t a1) {
  for (int i = 0; i < MAX_PENDING; i++) {
    if (g_pending[i].pid == pid || g_pending[i].pid == 0) {
      g_pending[i].pid = pid;
      g_pending[i].nr = nr;
      g_pending[i].arg0 = a0;
      g_pending[i].arg1 = a1;
      return;
    }
  }
}

static int pop_pending(pid_t pid, struct pending_exit *out) {
  for (int i = 0; i < MAX_PENDING; i++) {
    if (g_pending[i].pid == pid) {
      *out = g_pending[i];
      g_pending[i].pid = 0;
      return 1;
    }
  }
  return 0;
}

int main(int argc, char **argv) {
  if (argc < 2) {
    fprintf(stderr, "usage: %s <prog> [args...]\n", argv[0]);
    return 2;
  }

  pid_t child = fork();
  if (child < 0) {
    perror("fork");
    return 1;
  }
  if (child == 0) {
    if (ptrace(PTRACE_TRACEME, 0, NULL, NULL) < 0) {
      perror("PTRACE_TRACEME");
      _exit(127);
    }
    raise(SIGSTOP);
    if (install_realtime_seccomp() < 0) {
      perror("install_realtime_seccomp");
      _exit(127);
    }
    unsetenv("LD_PRELOAD");
    execvp(argv[1], &argv[1]);
    perror("execvp");
    _exit(127);
  }

  g_root_pid = child;
  struct sigaction sa;
  memset(&sa, 0, sizeof(sa));
  sa.sa_handler = forward_sig;
  sigaction(SIGTERM, &sa, NULL);
  sigaction(SIGINT, &sa, NULL);

  int status = 0;
  if (waitpid(child, &status, 0) < 0) {
    perror("waitpid initial");
    return 1;
  }
  long opts = PTRACE_O_TRACESYSGOOD | PTRACE_O_TRACEEXEC | PTRACE_O_TRACEFORK |
              PTRACE_O_TRACEVFORK | PTRACE_O_TRACECLONE |
              PTRACE_O_TRACESECCOMP | PTRACE_O_EXITKILL;
  if (ptrace(PTRACE_SETOPTIONS, child, 0, (void *)opts) < 0) {
    perror("PTRACE_SETOPTIONS");
    return 1;
  }
  ptrace(PTRACE_CONT, child, 0, 0);

  int root_exit_code = 0;
  for (;;) {
    int st = 0;
    pid_t pid = waitpid(-1, &st, __WALL);
    if (pid < 0) {
      if (errno == EINTR) {
        continue;
      }
      if (errno == ECHILD) {
        break;
      }
      perror("waitpid");
      break;
    }

    if (WIFEXITED(st)) {
      struct pending_exit dummy;
      pop_pending(pid, &dummy);
      if (pid == g_root_pid) {
        root_exit_code = WEXITSTATUS(st);
      }
      continue;
    }
    if (WIFSIGNALED(st)) {
      struct pending_exit dummy;
      pop_pending(pid, &dummy);
      if (pid == g_root_pid) {
        root_exit_code = 128 + WTERMSIG(st);
      }
      continue;
    }
    if (!WIFSTOPPED(st)) {
      continue;
    }

    int sig = WSTOPSIG(st);
    unsigned int event = (unsigned int)st >> 16;

    if (event == PTRACE_EVENT_EXEC) {
      strip_vdso_auxv(pid);
      ptrace(PTRACE_CONT, pid, 0, 0);
      continue;
    }
    if (event == PTRACE_EVENT_FORK || event == PTRACE_EVENT_VFORK ||
        event == PTRACE_EVENT_CLONE) {
      ptrace(PTRACE_CONT, pid, 0, 0);
      continue;
    }
    if (event == PTRACE_EVENT_SECCOMP) {
      struct user_regs_struct regs;
      if (ptrace(PTRACE_GETREGS, pid, NULL, &regs) == 0) {
        set_pending(pid, (long)regs.orig_rax, (uintptr_t)regs.rdi,
                    (uintptr_t)regs.rsi);
        ptrace(PTRACE_SYSCALL, pid, 0, 0);
      } else {
        ptrace(PTRACE_CONT, pid, 0, 0);
      }
      continue;
    }
    if (sig == (SIGTRAP | 0x80)) {
      struct pending_exit pe;
      if (pop_pending(pid, &pe)) {
        struct timespec ts;
        clock_gettime(CLOCK_REALTIME, &ts);
        if (pe.nr == __NR_clock_gettime && pe.arg1 != 0) {
          struct iovec loc = {.iov_base = &ts, .iov_len = sizeof(ts)};
          struct iovec rem = {.iov_base = (void *)pe.arg1,
                              .iov_len = sizeof(ts)};
          process_vm_writev(pid, &loc, 1, &rem, 1, 0);
        } else if (pe.nr == __NR_gettimeofday && pe.arg0 != 0) {
          struct timeval tv = {.tv_sec = ts.tv_sec,
                               .tv_usec = ts.tv_nsec / 1000};
          struct iovec loc = {.iov_base = &tv, .iov_len = sizeof(tv)};
          struct iovec rem = {.iov_base = (void *)pe.arg0,
                              .iov_len = sizeof(tv)};
          process_vm_writev(pid, &loc, 1, &rem, 1, 0);
        } else if (pe.nr == __NR_time) {
          struct user_regs_struct regs;
          if (ptrace(PTRACE_GETREGS, pid, NULL, &regs) == 0) {
            regs.rax = (unsigned long long)ts.tv_sec;
            ptrace(PTRACE_SETREGS, pid, NULL, &regs);
          }
          if (pe.arg0 != 0) {
            time_t tsec = ts.tv_sec;
            struct iovec loc = {.iov_base = &tsec, .iov_len = sizeof(tsec)};
            struct iovec rem = {.iov_base = (void *)pe.arg0,
                                .iov_len = sizeof(tsec)};
            process_vm_writev(pid, &loc, 1, &rem, 1, 0);
          }
        }
      }
      ptrace(PTRACE_CONT, pid, 0, 0);
      continue;
    }

    // Group-stop or newly cloned child initial SIGSTOP or normal signal delivery
    if (sig == SIGSTOP && pid != g_root_pid) {
      ptrace(PTRACE_CONT, pid, 0, 0);
    } else if (sig == SIGTRAP) {
      ptrace(PTRACE_CONT, pid, 0, 0);
    } else {
      ptrace(PTRACE_CONT, pid, 0, sig);
    }
  }
  return root_exit_code;
}
