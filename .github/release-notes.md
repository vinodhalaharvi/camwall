Install with Homebrew (FFmpeg comes with it):
```sh
brew install vinodhalaharvi/tap/camwall
```

- `camwall serve` shows every camera on one gap-free wall in your browser: a settings panel for screen shape, which feeds show and how big, tap a feed to fill the screen, 🔊 to listen to one live, and a password to share it on your network.
- `camwall stream` combines the feeds into one video with FFmpeg: open it in VLC, record it, restream it over RTSP, or go live on YouTube or Twitch over RTMP.
- `camwall record` (or `serve -record`) keeps every feed's sound in time-stamped files, with a Recordings page to find any moment.
- `camwall discover` finds ONVIF cameras on your network and prints their RTSP addresses; `camwall devices` lists this computer's cameras and screens.

Sources: IP cameras (RTSP, RTMP, SRT, UDP, HTTP), HLS, YouTube and other web video (with yt-dlp), webcams and capture devices, video files, GIFs and pictures, up to 25 per wall.

The binaries below are for macOS (Apple Silicon and Intel) and Linux (x86-64 and ARM64) and need FFmpeg 5 or later on your PATH. The macOS binaries are not notarised; if Gatekeeper blocks one, use Homebrew or run `xattr -d com.apple.quarantine camwall`.
