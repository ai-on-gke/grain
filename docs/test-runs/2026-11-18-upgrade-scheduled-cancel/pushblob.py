#!/usr/bin/env python3
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""pushblob.py TAG [MB]: push localhost:5000/acme/grain:TAG, a one-layer
image of MB (default 40) MiB of random bytes, straight to registry:2 over
its HTTP API. docker push would leave the layer in this host's containerd
content store, and a later pull of it would then finish at once however
slow slowreg.py makes the registry; pushed this way the host has never
seen the layer, so `docker pull localhost:5001/acme/grain:TAG` really
downloads it. The image never runs: it exists to be cancelled mid-pull.
"""

import gzip
import hashlib
import io
import json
import os
import sys
import tarfile
import urllib.request

REG = "http://localhost:5000"
REPO = "acme/grain"
tag = sys.argv[1]
size = int(sys.argv[2] if len(sys.argv) > 2 else 40) * 1024 * 1024


def push_blob(data):
    digest = "sha256:" + hashlib.sha256(data).hexdigest()
    req = urllib.request.Request(f"{REG}/v2/{REPO}/blobs/uploads/", method="POST")
    loc = urllib.request.urlopen(req).headers["Location"]
    if loc.startswith("/"):
        loc = REG + loc
    sep = "&" if "?" in loc else "?"
    req = urllib.request.Request(f"{loc}{sep}digest={digest}", data=data, method="PUT",
                                 headers={"Content-Type": "application/octet-stream"})
    urllib.request.urlopen(req)
    return digest


raw = io.BytesIO()
with tarfile.open(fileobj=raw, mode="w") as tf:
    blob = os.urandom(size)
    info = tarfile.TarInfo("blob")
    info.size = len(blob)
    tf.addfile(info, io.BytesIO(blob))
tar = raw.getvalue()
layer = gzip.compress(tar, compresslevel=1)
config = json.dumps({
    "architecture": "amd64", "os": "linux",
    "config": {"Labels": {"walk.tag": tag}},
    "rootfs": {"type": "layers", "diff_ids": ["sha256:" + hashlib.sha256(tar).hexdigest()]},
}).encode()
layer_digest = push_blob(layer)
config_digest = push_blob(config)
manifest = json.dumps({
    "schemaVersion": 2,
    "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
    "config": {"mediaType": "application/vnd.docker.container.image.v1+json",
               "size": len(config), "digest": config_digest},
    "layers": [{"mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
                "size": len(layer), "digest": layer_digest}],
}).encode()
req = urllib.request.Request(f"{REG}/v2/{REPO}/manifests/{tag}", data=manifest, method="PUT",
                             headers={"Content-Type": "application/vnd.docker.distribution.manifest.v2+json"})
resp = urllib.request.urlopen(req)
print(tag, resp.headers.get("Docker-Content-Digest"), f"{len(layer)} bytes")
