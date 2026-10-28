//! Experimental, read-only vhost-user-blk server for granule's extent layout
//! (`manifest.json` + `extents/%08d.raw`, absent extents read as zeros), built
//! on the rust-vmm stack cloud-hypervisor uses: vhost, vhost-user-backend,
//! virtio-queue and vm-memory.
//!
//! It exists to answer one question -- would writing granule's Go server in
//! Rust make random reads faster? -- so it does the same work per request the
//! Go server does (one cached O_RDONLY fd per extent, a negative cache for
//! absent ones, one preadv(2) straight into guest memory) on the stock
//! rust-vmm design: one epoll thread per virtqueue serving its requests in
//! order. See benchmarks/storage/results/vhost-user-random-read.md.
//!
//!   vhost-user-extents-rs --dir disk.extents --socket /tmp/s.sock [--queues 4]

use std::fs::File;
use std::io::{self, Result};
use std::os::unix::io::AsRawFd;
use std::path::PathBuf;
use std::sync::{Arc, OnceLock, RwLock};

use vhost::vhost_user::message::{VhostUserProtocolFeatures, VhostUserVirtioFeatures};
use vhost_user_backend::{VhostUserBackend, VhostUserDaemon, VringRwLock, VringT};
use virtio_queue::QueueT;
use vm_memory::{
    Address, Bytes, GuestAddress, GuestAddressSpace, GuestMemoryAtomic, GuestMemoryBackend,
    GuestMemoryMmap,
};
use vmm_sys_util::epoll::EventSet;

const SECTOR: u64 = 512;
const VIRTIO_F_VERSION_1: u64 = 32;
const VIRTIO_RING_F_EVENT_IDX: u64 = 29;
const VIRTIO_BLK_F_SEG_MAX: u64 = 2;
const VIRTIO_BLK_F_RO: u64 = 5;
const VIRTIO_BLK_F_BLK_SIZE: u64 = 6;
const VIRTIO_BLK_F_FLUSH: u64 = 9;
const VIRTIO_BLK_F_MQ: u64 = 12;
const VIRTIO_BLK_T_IN: u32 = 0;
const VIRTIO_BLK_T_FLUSH: u32 = 4;
const VIRTIO_BLK_T_GET_ID: u32 = 8;
const S_OK: u8 = 0;
const S_IOERR: u8 = 1;
const S_UNSUPP: u8 = 2;
const QUEUE_SIZE: usize = 1024;

/// The extent directory, opened lazily: each extent's fd is opened once and
/// kept (None once it is known to be absent), so after warm-up a read is one
/// preadv and no lock.
struct Extents {
    dir: PathBuf,
    size: u64,
    extent: u64,
    fds: Vec<OnceLock<Option<File>>>,
}

fn manifest_field(json: &str, key: &str) -> Option<u64> {
    let at = json.find(&format!("\"{key}\""))? + key.len() + 2;
    let rest = json[at..].trim_start().strip_prefix(':')?.trim_start();
    let end = rest.find(|c: char| !c.is_ascii_digit()).unwrap_or(rest.len());
    rest[..end].parse().ok()
}

impl Extents {
    fn open(dir: PathBuf) -> io::Result<Self> {
        let json = std::fs::read_to_string(dir.join("manifest.json"))?;
        let bad = || io::Error::new(io::ErrorKind::InvalidData, "bad manifest.json");
        let size = manifest_field(&json, "virtualSizeBytes").ok_or_else(bad)?;
        let extent = manifest_field(&json, "extentSizeBytes").ok_or_else(bad)?;
        if extent == 0 {
            return Err(bad());
        }
        let n = size.div_ceil(extent) as usize;
        Ok(Extents { dir, size, extent, fds: (0..n).map(|_| OnceLock::new()).collect() })
    }

    fn fd(&self, idx: usize) -> Option<&File> {
        self.fds[idx]
            .get_or_init(|| File::open(self.dir.join("extents").join(format!("{idx:08}.raw"))).ok())
            .as_ref()
    }

    /// Fills iov from byte offset off. Each run of iovecs inside one extent
    /// is one preadv; past a short or absent extent reads as zeros.
    fn readv(&self, iov: &mut [libc::iovec], mut off: u64) -> io::Result<()> {
        let total: u64 = iov.iter().map(|v| v.iov_len as u64).sum();
        if off.checked_add(total).map_or(true, |end| end > self.size) {
            return Err(io::Error::from_raw_os_error(libc::EINVAL));
        }
        let mut i = 0;
        while i < iov.len() {
            let idx = (off / self.extent) as usize;
            let ext_end = (idx as u64 + 1) * self.extent;
            // Take whole iovecs while they stay inside this extent; split one
            // that straddles the boundary.
            let mut j = i;
            let mut len = 0u64;
            while j < iov.len() && off + len + iov[j].iov_len as u64 <= ext_end {
                len += iov[j].iov_len as u64;
                j += 1;
            }
            let mut tail = None;
            if j == i {
                let first = (ext_end - off) as usize;
                let v = iov[i];
                iov[i].iov_len = first;
                tail = Some(libc::iovec {
                    iov_base: unsafe { (v.iov_base as *mut u8).add(first) } as *mut _,
                    iov_len: v.iov_len - first,
                });
                len = first as u64;
                j = i + 1;
            }
            self.read_run(idx, &mut iov[i..j], off % self.extent, len)?;
            off += len;
            match tail {
                // iov[i] is now the remainder; carry on from it.
                Some(t) => iov[i] = t,
                None => i = j,
            }
        }
        Ok(())
    }

    fn read_run(&self, idx: usize, iov: &mut [libc::iovec], ext_off: u64, len: u64) -> io::Result<()> {
        let mut got = 0u64;
        if let Some(f) = self.fd(idx) {
            let mut v: &mut [libc::iovec] = iov;
            while got < len {
                let n = unsafe {
                    libc::preadv(f.as_raw_fd(), v.as_ptr(), v.len() as i32, (ext_off + got) as i64)
                };
                if n < 0 {
                    let e = io::Error::last_os_error();
                    if e.kind() == io::ErrorKind::Interrupted {
                        continue;
                    }
                    return Err(e);
                }
                if n == 0 {
                    break;
                }
                got += n as u64;
                let mut n = n as usize;
                while !v.is_empty() && n >= v[0].iov_len {
                    n -= v[0].iov_len;
                    v = &mut v[1..];
                }
                if n > 0 {
                    v[0].iov_base = unsafe { (v[0].iov_base as *mut u8).add(n) } as *mut _;
                    v[0].iov_len -= n;
                }
            }
            for x in v.iter() {
                unsafe { std::ptr::write_bytes(x.iov_base as *mut u8, 0, x.iov_len) };
            }
        } else {
            for x in iov.iter() {
                unsafe { std::ptr::write_bytes(x.iov_base as *mut u8, 0, x.iov_len) };
            }
        }
        Ok(())
    }
}

type Mem = GuestMemoryAtomic<GuestMemoryMmap>;

struct Backend {
    ext: Extents,
    queues: usize,
    event_idx: RwLock<bool>,
    mem: RwLock<Option<Mem>>,
}

impl Backend {
    fn process(&self, vring: &VringRwLock<Mem>) -> io::Result<()> {
        let mem = self.mem.read().unwrap().clone().unwrap().memory();
        let ioerr = |e| io::Error::new(io::ErrorKind::Other, format!("{e:?}"));
        let mut iov: Vec<libc::iovec> = Vec::with_capacity(256);
        loop {
            let chain = vring.get_mut().get_queue_mut().pop_descriptor_chain(mem.clone());
            let Some(chain) = chain else { break };
            let head = chain.head_index();
            iov.clear();
            let mut hdr = [0u8; 16];
            let mut hdr_got = 0usize;
            let mut status_addr = None;
            let mut ok = true;
            let mut data_len = 0u32;
            for d in chain {
                if !d.is_write_only() {
                    if hdr_got < 16 {
                        let n = (16 - hdr_got).min(d.len() as usize);
                        ok &= mem.read_slice(&mut hdr[hdr_got..hdr_got + n], d.addr()).is_ok();
                        hdr_got += n;
                    }
                    continue;
                }
                // The last byte of the last writable descriptor is the status.
                if let Some((a, l)) = status_addr.take() {
                    ok &= push_iov(&mem, &mut iov, a, l);
                    data_len += l;
                }
                status_addr = Some((d.addr(), d.len()));
            }
            let mut written = 0u32;
            let status = match (ok && hdr_got == 16, status_addr) {
                (true, Some((a, l))) => {
                    if l > 1 {
                        ok &= push_iov(&mem, &mut iov, a, l - 1);
                        data_len += l - 1;
                    }
                    let status_at = a.unchecked_add(u64::from(l) - 1);
                    let typ = u32::from_le_bytes(hdr[0..4].try_into().unwrap());
                    let sector = u64::from_le_bytes(hdr[8..16].try_into().unwrap());
                    let s = match typ {
                        _ if !ok => S_IOERR,
                        VIRTIO_BLK_T_IN => match self.ext.readv(&mut iov, sector * SECTOR) {
                            Ok(()) => {
                                written = data_len;
                                S_OK
                            }
                            Err(_) => S_IOERR,
                        },
                        VIRTIO_BLK_T_FLUSH => S_OK,
                        VIRTIO_BLK_T_GET_ID => {
                            let mut id = [0u8; 20];
                            id[..15].copy_from_slice(b"granule-extents");
                            let mut left: &[u8] = &id;
                            for v in &iov {
                                let n = v.iov_len.min(left.len());
                                unsafe { std::ptr::copy_nonoverlapping(left.as_ptr(), v.iov_base as *mut u8, n) };
                                left = &left[n..];
                            }
                            written = data_len;
                            S_OK
                        }
                        _ => S_UNSUPP,
                    };
                    let _ = mem.write_slice(&[s], status_at);
                    s
                }
                _ => S_IOERR,
            };
            let _ = status;
            vring.add_used(head, written + 1).map_err(ioerr)?;
        }
        if vring.needs_notification().map_err(ioerr)? {
            vring.signal_used_queue()?;
        }
        Ok(())
    }
}

fn push_iov(mem: &GuestMemoryMmap, iov: &mut Vec<libc::iovec>, a: GuestAddress, l: u32) -> bool {
    // get_slice checks [a, a+l) lies inside one region.
    match mem.get_slice(a, l as usize) {
        Ok(s) => {
            iov.push(libc::iovec { iov_base: s.ptr_guard_mut().as_ptr() as *mut _, iov_len: l as usize });
            true
        }
        Err(_) => false,
    }
}

impl VhostUserBackend for Backend {
    type Bitmap = ();
    type Vring = VringRwLock<Mem>;

    fn num_queues(&self) -> usize {
        self.queues
    }
    fn max_queue_size(&self) -> usize {
        QUEUE_SIZE
    }
    fn features(&self) -> u64 {
        [VIRTIO_F_VERSION_1, VIRTIO_RING_F_EVENT_IDX, VIRTIO_BLK_F_SEG_MAX, VIRTIO_BLK_F_RO,
         VIRTIO_BLK_F_BLK_SIZE, VIRTIO_BLK_F_FLUSH, VIRTIO_BLK_F_MQ]
            .iter()
            .fold(VhostUserVirtioFeatures::PROTOCOL_FEATURES.bits(), |f, b| f | 1 << b)
    }
    fn protocol_features(&self) -> VhostUserProtocolFeatures {
        VhostUserProtocolFeatures::MQ | VhostUserProtocolFeatures::CONFIG | VhostUserProtocolFeatures::REPLY_ACK
    }
    fn set_event_idx(&self, enabled: bool) {
        *self.event_idx.write().unwrap() = enabled;
    }
    fn get_config(&self, offset: u32, size: u32) -> Vec<u8> {
        let mut cfg = [0u8; 60];
        cfg[0..8].copy_from_slice(&(self.ext.size / SECTOR).to_le_bytes());
        cfg[12..16].copy_from_slice(&(QUEUE_SIZE as u32 - 2).to_le_bytes()); // seg_max
        cfg[20..24].copy_from_slice(&512u32.to_le_bytes()); // blk_size
        cfg[34..36].copy_from_slice(&(self.queues as u16).to_le_bytes()); // num_queues
        let (o, s) = (offset as usize, size as usize);
        cfg.get(o..o + s).map(|c| c.to_vec()).unwrap_or_default()
    }
    fn update_memory(&self, mem: Mem) -> Result<()> {
        *self.mem.write().unwrap() = Some(mem);
        Ok(())
    }
    fn queues_per_thread(&self) -> Vec<u64> {
        (0..self.queues).map(|i| 1u64 << i).collect()
    }
    fn handle_event(&self, device_event: u16, evset: EventSet, vrings: &[VringRwLock<Mem>], _thread: usize) -> Result<()> {
        if evset != EventSet::IN {
            return Err(io::Error::from_raw_os_error(libc::EINVAL));
        }
        let vring = vrings.get(device_event as usize).ok_or_else(|| io::Error::from_raw_os_error(libc::EINVAL))?;
        if *self.event_idx.read().unwrap() {
            // Standard event-idx loop: keep notifications off while draining,
            // and drain again if requests arrived while turning them on.
            loop {
                vring.disable_notification().unwrap();
                self.process(vring)?;
                if !vring.enable_notification().unwrap() {
                    break;
                }
            }
        } else {
            self.process(vring)?;
        }
        Ok(())
    }
}

fn main() {
    let mut dir = None;
    let mut socket = None;
    let mut queues = 4usize;
    let mut args = std::env::args().skip(1);
    while let Some(a) = args.next() {
        match a.as_str() {
            "--dir" => dir = args.next().map(PathBuf::from),
            "--socket" => socket = args.next(),
            "--queues" => queues = args.next().and_then(|q| q.parse().ok()).unwrap_or(4),
            _ => panic!("usage: vhost-user-extents-rs --dir DIR --socket PATH [--queues N]"),
        }
    }
    let (dir, socket) = (dir.expect("--dir"), socket.expect("--socket"));
    let backend = Arc::new(Backend {
        ext: Extents::open(dir).expect("open extent dir"),
        queues,
        event_idx: RwLock::new(false),
        mem: RwLock::new(None),
    });
    // One frontend at a time; serve the next once it disconnects.
    loop {
        let mem = GuestMemoryAtomic::new(GuestMemoryMmap::new());
        *backend.mem.write().unwrap() = Some(mem.clone());
        let mut daemon = VhostUserDaemon::new("vhost-user-extents-rs".into(), backend.clone(), mem)
            .expect("create daemon");
        if let Err(e) = daemon.serve(&socket) {
            eprintln!("vhost-user-extents-rs: {e:?}");
        }
    }
}
