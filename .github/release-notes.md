Install with Homebrew (FFmpeg comes with it):
```sh
brew install vinodhalaharvi/tap/camwall
```

- `camwall serve` shows every camera on one gap-free wall in your browser: pin a feed to make it bigger, click one to fill the screen, share it on your network with a password.
- `camwall stream` combines the feeds into one video with FFmpeg: open it in VLC, record it, or restream it over RTSP, RTMP, SRT or HLS.
- `camwall plan` prints the wall for any screen shape; `camwall laws` checks the layout algebra against brute force.

Sources can be RTSP cameras, network streams (RTMP, SRT, UDP, HLS), video files, GIFs and still images, up to 25 per wall.

Binaries below are for macOS (Apple Silicon and Intel) and Linux (x86-64 and ARM64); they need FFmpeg 5 or later on your PATH. The macOS binaries are not notarised; if Gatekeeper blocks one, use Homebrew or run `xattr -d com.apple.quarantine camwall`.
