**0.1.3**

- `camwall demo`: try camwall with no cameras. Six pet clips (3.5 MB, downloaded once) play as cameras next to a still picture; offline, FFmpeg's moving test pictures stand in.
- Walls of many different shapes (a folder of photos) lay out in under a second instead of failing with "too many different feed sizes": close shapes are grouped, each feed still shown whole.
- Up to 16 feeds per wall (was 25): about what a laptop decodes comfortably.
- Webcams and screens no longer hold up the start for 20 seconds, and camwall says which feeds it is waiting for (and to allow camera or screen-recording permission).

**0.1.2** comes from running every supported source through one wall (RTSP, RTMP, SRT, UDP, HLS, MJPEG over HTTP, snapshot URLs, a dozen video formats, animations, pictures and the screen):

- MJPEG cameras over HTTP showed about one frame a second; they now show every frame asked for.
- RTMP streams sometimes failed to start; they now get time to announce their video.
- A recorded HLS playlist played far too fast and then stopped; it now plays at its own speed and loops.
- MPEG-TS files stopped after one play; they now loop.
- A camera's snapshot.jpg address showed one picture for ever; the browser wall now fetches it every 2 seconds.
- UDP streams sent to this computer failed when recording was on, since only one program can receive them; they are now shown and left out of recording, with a note.
- Video files' sound is no longer recorded (the files already keep it).

**0.1.1** fixes what two independent reviews found:

- Safer by default: camera passwords and tokens are hidden in messages; without `-password` the wall answers only to IP addresses and local names; `discover` no longer trusts what a camera says about other hosts; the user name for `-password` is `camwall`, as documented.
- Steadier: stopping camwall stops every FFmpeg it started (even if camwall is killed); a feed never runs two FFmpegs at once; silent UDP/SRT sources time out and retry; a big or odd request can't run the server out of memory.
- Better walls of 7 to 10 feeds (more ways to split are searched), and very varied sizes are evened out instead of refused.
- Recordings: the hour that repeats when clocks go back no longer overwrites files, times are shown on the recording computer's clock, and old files are pruned only in camwall's own folders.
- Browser: errors show as a note that clears itself, hidden feeds can't stay zoomed, smoother live sound, keyboard zoom for tiles, a way out of the hidden-bar mode on phones, and browser shortcuts like Ctrl+S are left alone.
- Labels with apostrophes or % signs, feeds files with tabs, and duplicate feed names all work.

Install with Homebrew (FFmpeg comes with it):
```sh
brew install vinodhalaharvi/tap/camwall
```

- `camwall serve` shows every camera on one gap-free wall in your browser: a settings panel for screen shape, which feeds show and how big, tap a feed to fill the screen, 🔊 to listen to one live, and a password to share it on your network.
- `camwall stream` combines the feeds into one video with FFmpeg: open it in VLC, record it, restream it over RTSP, or go live on YouTube or Twitch over RTMP.
- `camwall record` (or `serve -record`) keeps every feed's sound in time-stamped files, with a Recordings page to find any moment.
- `camwall discover` finds ONVIF cameras on your network and prints their RTSP addresses; `camwall devices` lists this computer's cameras and screens.

Sources: IP cameras (RTSP, RTMP, SRT, UDP, HTTP), HLS, YouTube and other web video (with yt-dlp), webcams and capture devices, video files, GIFs and pictures, up to 16 per wall.

The binaries below are for macOS (Apple Silicon and Intel) and Linux (x86-64 and ARM64) and need FFmpeg 5 or later on your PATH. The macOS binaries are not notarised; if Gatekeeper blocks one, use Homebrew or run `xattr -d com.apple.quarantine camwall`.
