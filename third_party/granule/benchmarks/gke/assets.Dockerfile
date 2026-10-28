# Packages the benchmarks/kernel/build.sh output so it can be staged onto
# a GKE node's local filesystem via stage-assets-job.yaml, mirroring how a
# real deployment would pre-populate a node-local image cache (see the
# main README's "How it works" section: granule never fetches images
# itself).
FROM busybox:stable@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662
COPY vmlinux /assets/vmlinux
COPY initramfs.img /assets/initramfs.img
COPY disk.img /assets/disk.img
