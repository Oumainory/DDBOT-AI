# Third-party notices

DDBOT-AI includes or derives from open-source components listed in the Go and
Node lockfiles. Their copyright, license text and source attribution remain in
the corresponding upstream package metadata. Release archives must include
this notice together with `LICENSE` and `NOTICE`; the release workflow must
refresh this inventory when dependencies change.

FFmpeg, when present in a Full artifact, is a separate executable and is built
from the pinned BtbN/FFmpeg-Builds LGPL static asset recorded in
`release/ffmpeg.json`. Full artifacts include `FFMPEG-PROVENANCE.txt` and the
corresponding `FFMPEG-LGPL-2.1.txt` text from the immutable FFmpeg source
commit. The main DDBOT-AI binary does not link FFmpeg libraries.

The BtbN/FFmpeg-Builds build repository and its build tooling are distributed
under their own MIT terms. That MIT license applies to the build provider, not
to the bundled FFmpeg executable. The bundled executable is an LGPL build, and
any codecs or other libraries present in that build retain their applicable
upstream licenses. DDBOT-AI does not relicense those independent components.
