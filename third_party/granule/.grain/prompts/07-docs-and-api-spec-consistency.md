# Nightly Prompt: Documentation, API & Specification Consistency Audit

## Objective
Perform an automated audit across all repository documentation (`README.md`, `docs/granulefile.md`, `docs/guest-images.md`, `docs/agent-substrate-uvm-runtime.md`, `docs/contributing.md`, `deploy/k8s/gke.md`) and compare against actual CLI commands, subcommands, flags, and YAML schemas to prevent documentation drift and eliminate stale references.

## Context & Audit Scope
Documentation accuracy is critical for developer ergonomics. Key areas to audit nightly:
1. **CLI Commands & Flags**: Ensure every command and flag documented in `README.md` and `docs/` exactly matches `pkg/cli/cli.go` and subcommand flag definitions.
2. **Project Naming Consistency**: Ensure no obsolete project names (e.g. `kontur`, `konturctl`, `Konturfile`) appear in active user documentation or code, outside of historical notes/renaming notices.
3. **Specification Alignment**: Ensure `docs/granulefile.md` §12 implementation status accurately reflects what `pkg/granulefile` implements.
4. **Code Blocks & Examples**: Verify that code snippets in documentation parse correctly and do not use invalid syntax.

## Execution Steps

### 1. CLI Usage & Flags Verification
Verify documented CLI commands against `granule --help`:
```bash
go build -o /tmp/granule ./cmd/granule

# Extract CLI usage output
/tmp/granule --help

# Verify documented subcommands exist
for cmd in "vm create" "vm run" "vm exec" "vm shell" "vm wait" "vm status" "vm update" "vm delete" "vm list" \
           "guest build" "guest pack" "guest rebase" "guest verify" \
           "granulefile check" "granulefile build"; do
  if ! /tmp/granule --help | grep -Fq "$cmd"; then
    echo "ERROR: Documented subcommand '$cmd' missing from CLI usage text"
    exit 1
  fi
done
echo "PASS: All documented subcommands match CLI usage"
```

### 2. Check for Stale Project References
Search the codebase for inadvertent references to legacy names:
```bash
# Check for obsolete name occurrences in active doc markdown files
grep -rnEi "\bkontur\b|\bkonturctl\b|\bKonturfile\b" docs/ README.md deploy/ || true
```
- Verify that any remaining occurrences are strictly within explicit historical migration notes and not in command examples.

### 3. Granulefile Example Validation
Extract Granulefile snippets from `README.md` and `docs/granulefile.md` and validate them with `granule granulefile check`:
```bash
python3 -c '
import re, subprocess, tempfile

def check_snippet(title, snippet):
    with tempfile.NamedTemporaryFile("w", suffix=".Granulefile") as f:
        f.write(snippet)
        f.flush()
        res = subprocess.run(["/tmp/granule", "granulefile", "check", "-f", f.name], capture_output=True, text=True)
        if res.returncode != 0:
            print(f"WARN: Snippet [{title}] failed check:\n{res.stderr}")
        else:
            print(f"PASS: Snippet [{title}] validated successfully")

# Validate spec examples
with open("README.md") as f:
    content = f.read()
    # Find dockerfile/granulefile blocks
    matches = re.findall(r"```(?:dockerfile|granulefile)\n(.*?)```", content, re.DOTALL)
    for idx, snippet in enumerate(matches):
        check_snippet(f"README snippet {idx+1}", snippet)
'
```

## Evaluation & Reporting Criteria
1. **Audit Summary**: Provide a line-by-line summary of verified CLI flags, code snippets, and document links.
2. **Drift Detection**: Flag any discrepancy where CLI flag names or defaults have diverged from documentation.
3. **Stale References**: List any unapproved legacy project references found.
4. **Remediation**: Provide diffs to update documentation or fix CLI usage strings.
