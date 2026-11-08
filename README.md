# camwall

Every camera feed on one gap-free wall: in your browser, or as one combined video stream.

camwall is for a shop manager who wants all their cameras in one place. Give it IP cameras, network streams, web videos, the computer's own camera, video files, animations or a still image or two. It finds the arrangement that shows the smallest feed as large as possible on your screen, and no feed is cropped or stretched.

![Six pet cameras and a still photo on one wall](docs/wall.jpg)

```sh
camwall serve rtsp://192.168.1.20/stream1 rtsp://192.168.1.21/stream1 rtsp://192.168.1.22/stream1 logo.png
```

![Dragging the screen shape: the wall re-solves on every frame](docs/demo.gif)

## Install

camwall needs [FFmpeg](https://ffmpeg.org) 5 or later; 8 or later plays animated WebP too. [yt-dlp](https://github.com/yt-dlp/yt-dlp) is optional, for YouTube and other web video. Build camwall with Go 1.24:

```sh
brew install ffmpeg yt-dlp        # or your system's package manager
go install github.com/vinodhalaharvi/camwall/cmd/camwall@latest
```

`camwall help` lists every command and option.

## Find your cameras

Most IP cameras speak ONVIF. `camwall discover` finds them on your network and asks each one for its RTSP address:

```sh
camwall discover -user admin -password secret > cams.txt
```

Add `-sub` to use each camera's lighter sub-stream, which is plenty for a wall. `camwall devices` lists the cameras and screens attached to this computer.

## What it can show

| Source | Write it as |
|---|---|
| IP camera (RTSP, RTMP, SRT, UDP, HTTP/MJPEG) | `rtsp://user:pass@192.168.1.20/stream1` |
| HLS or another network stream | `https://example.com/live/index.m3u8` |
| YouTube, Twitch, Vimeo and other web video, live or recorded | the page URL, e.g. `https://www.youtube.com/watch?v=…`, or `ytdl:URL` for any site (needs [yt-dlp](https://github.com/yt-dlp/yt-dlp)) |
| This computer's camera or screen | `webcam`, `webcam:1`, `screen` |
| Any FFmpeg capture device | `v4l2:/dev/video2`, `avfoundation:1`, `"dshow:video=Logitech C920"`, `decklink:DeckLink Mini Recorder` |
| Video files, looped | `lobby.mp4`, `.mov`, `.mkv`, `.avi`, `.ts`, `.webm`, … |
| Animations | `promo.gif`, animated `.png` and `.webp` |
| Pictures | `.jpg`, `.png`, `.webp`, `.avif`, `.tiff`, `.bmp`, `.heic` |

Web pages are resolved again each time they connect, since their video addresses expire. Pictures browsers can't show, such as TIFF, BMP and HEIC, are converted to JPEG once at start-up; on a Mac, HEIC goes through `sips` if FFmpeg can't read it. RTSP tries TCP first and falls back to UDP for cameras that only offer UDP.

**Cloud-only cameras** like Ring, Nest, Arlo, Blink and Wyze have no stream address of their own. A bridge such as [go2rtc](https://github.com/AlexxIT/go2rtc), [Scrypted](https://www.scrypted.app) or [docker-wyze-bridge](https://github.com/mrlt8/docker-wyze-bridge) re-publishes them as RTSP, and camwall reads that. The same goes for WebRTC-only sources.

## Your cameras in a file

List sources one per line, with an optional name, size and shape:

```
# cams.txt
rtsp://admin:secret@192.168.1.20/Streaming/101   name=Front door  weight=2
rtsp://admin:secret@192.168.1.21/Streaming/101   name=Register
rtsp://192.168.1.22:554/live                     name=Stockroom
https://example.com/lobby/index.m3u8             name=Lobby
promo.gif                                        name=This week
floorplan.png                                    name=Floor plan  aspect=4:3
```

- `name=` is shown on the tile. It takes the rest of the line, apart from any `weight=` or `aspect=` that follow it.
- `weight=N` asks for N times the area of the other feeds. The wall only changes if a bigger tile for that feed doesn't shrink the others too much.
- `aspect=W:H` sets the shape and skips asking FFmpeg. Otherwise camwall reads each feed's size at start-up, and a camera that's offline at start-up is assumed to be 16:9.
- `filter=` applies any FFmpeg video filter before the feed goes on the wall: `crop=iw/2:ih:0:0`, `hflip`, `transpose=1`, or `v360=fisheye:e:ih_fov=180:iv_fov=180` to dewarp a fisheye camera. camwall reads the shape after the filter.
- `transport=udp` or `transport=tcp` pins how an RTSP camera is read. The default, `auto`, tries TCP and falls back to UDP.
- `format=`, `size=1280x720` and `fps=30` set capture options for devices. A source with spaces goes in double quotes.

Then run `camwall serve -f cams.txt`.

Camera passwords stay on the machine running camwall. Browsers only ever see the names.

## Watch in a browser

```sh
camwall serve -f cams.txt                        # this computer only
camwall serve -f cams.txt -addr :7788 -password s3cret   # TVs, tablets and phones on your network
```

- **Settings** (S) opens a panel: pick a fixed screen shape for a TV of another shape, turn each feed on or off and set its size, and change the gap, background and names. Settings are kept in each browser.
- **★** on a tile makes that feed bigger, and the wall re-solves around it.

![Settings: the back yard set to 4×, and the wall re-solved around it](docs/settings.jpg)

- **Click** a tile to fill the screen with it. Click again, or press Esc, to go back.
- **Resize** the window, or turn a phone, and the wall re-arranges for the new shape.
- **Full screen** (F) and **Hide bar** (K) turn a spare screen into a monitor wall. Open `/?kiosk` to start that way.
- A tile shows **Reconnecting…** when its camera drops, and picks up again by itself.

- **🔊** on a tile with sound plays that camera live, one camera at a time, about a quarter of a second behind. **Stop** (M) in the bar ends it.

| Key | Does |
|---|---|
| S | open or close Settings |
| F | full screen |
| K | hide or show the bar (kiosk) |
| N | show or hide names |
| M | stop listening |
| Esc | leave a zoomed camera, then kiosk, then Settings |

Open `/?kiosk` to start with the bar hidden, or `/?nonames` to start without names.

Each camera is opened once, however many people are watching, and only while someone is watching. All feeds reach the page over a single connection, so a wall of 20 cameras isn't limited by the browser's six connections per server. With `-password`, viewers sign in with the user name `camwall`.

## Keep the sound for later

```sh
camwall serve -f cams.txt -record recordings              # wall and recording together
camwall record -f cams.txt -o recordings -keep 30d        # recording only, e.g. on a spare computer
```

Every feed with a microphone is recorded continuously, whether or not anyone is watching. Files are cut every 15 minutes on the clock (`-segment`) and named by when they start, in a folder per camera:

```
recordings/01 Front door/2026-11-08_14-15-00.m4a
recordings/01 Front door/2026-11-08_14-30-00.m4a
recordings/03 Register/2026-11-08_14-15-00.m4a
```

So "what happened at 14:37" is the 14:30 file, seven minutes in. The **Recordings** button on the wall opens a page that does this for you: pick a date and time, and every camera's recording from that moment is cued five seconds before it.

![The Recordings page: find what was recorded at a moment](docs/recordings.jpg)

Files are AAC in fragmented MP4, and they play in any browser, QuickTime, VLC or Windows Media Player. They're written a second at a time, so a power cut loses at most the last second. Recordings older than `-keep` (default `7d`; `0` keeps everything) are deleted. Sound takes about 21 MB per camera per hour. Cameras that are offline at start-up are retried until they come back; a camera without a microphone is skipped.

Recording sound can be regulated where you are, for example needing signs or the consent of the people recorded. Check the rules that apply to your shop.

## One combined stream

`camwall stream` makes the same wall as a single H.264 video using FFmpeg:

```sh
camwall stream -f cams.txt -o - | vlc -                    # watch in VLC
camwall stream -f cams.txt -o wall.mkv                     # record
camwall stream -f cams.txt -o rtsp://localhost:8554/wall   # restream (e.g. to MediaMTX)
camwall stream -f cams.txt -o rtmp://a.rtmp.youtube.com/live2/YOUR-KEY   # YouTube Live
camwall stream -f cams.txt -o srt://0.0.0.0:9000?mode=listener
camwall stream -f cams.txt -o /var/www/wall/index.m3u8     # HLS for any browser or TV
camwall stream -f cams.txt -dry-run                        # print the FFmpeg command
```

`-size 3840x2160` sets the output size and `-fps` the frame rate. `-labels` draws names, which needs FFmpeg built with `drawtext`. `-audio 3` carries feed 3's sound into the stream. `-encoder` picks the video encoder: `libx265`, or a hardware one such as `h264_videotoolbox` on a Mac, `h264_nvenc` or `h264_qsv`, with `-bitrate`. If FFmpeg stops, for example because a camera dropped out, camwall restarts it.

For RTMP destinations such as YouTube Live, Twitch or Facebook Live, camwall sends what these platforms expect: a constant bitrate sized to the output (4 Mbps at 720p, 6 Mbps at 1080p, or `-bitrate`), keyframes every 2 seconds, and a sound track. That's a silent one unless you pick a feed with `-audio`, because YouTube rejects streams without sound. YouTube's first live stream on a channel can take up to 24 hours to be enabled, and it ends streams after 12 hours; camwall reconnects by itself. For shop cameras, make the stream Unlisted or Private.

All feeds share one clock. FFmpeg opens cameras one at a time, which can take several seconds, and the first cameras build up video in the meantime. Each packet is stamped with the time it arrives, and only the newest frame in each slot is kept. So every tile shows the present moment, rather than the first cameras running seconds behind the last.

## See the wall without running it

```
$ camwall plan -f cams.txt          # (sketch trimmed)
  +----------------------+---------------------+----------------------+
  |          1           |          2          |          3           |
  +-------------+--------+----------+----------+---+------------------+
  |      9      |         7         |     10       |        8         |
  +-------------+--------+----------+----------+---+------------------+
  |          4           |          5          |          6           |
  +----------------------+---------------------+----------------------+

Wall fills 95% of a 16:9 screen; the smallest feed gets 7.4% of it.
```

## How the wall is chosen

The layout is a dynamic program written with [dyna](https://github.com/vinodhalaharvi/dyna), the same library behind [collage](https://github.com/vinodhalaharvi/collage). It searches every *guillotine* wall, meaning every way to split the screen in two, then each part in two, and so on.

- **Feeds are grouped into kinds.** Cameras that share a shape and a weight are interchangeable, so the search tracks how many of each kind are left rather than which ones. Sixteen identical cameras give 17 subproblems instead of 65,536, which is why camwall handles 25 feeds where collage stops at 10.
- **Nothing is cropped.** Each tile keeps its feed's exact shape, and the wall is letterboxed into the screen. The score is the on-screen area of the smallest feed per unit of weight. It's maximised with the (max, min) semiring, graded by aspect ratio, so fitting the wall to a new screen shape is a lookup rather than a new search.
- **Big walls stay fast.** Groups of up to 6 feeds are split every possible way. Larger groups only split off blocks of one kind, such as a row of identical cameras. On the walls we compared against the full search, this lost at most 5% of the smallest feed's size and was usually exact.

`camwall laws` checks that the algebra obeys the semiring laws that make this correct, and compares the dynamic program with brute force.

## Privacy and security

- `camwall serve` listens only on this computer unless you give `-addr :7788`. On a shop network, add `-password`; it protects the wall, the live sound and the recordings alike.
- Camera addresses and their passwords stay on the computer running camwall. Browsers get only the names you give the feeds, and the pictures.
- camwall serves plain HTTP. To watch from outside the shop, use a private network such as [Tailscale](https://tailscale.com) or a tunnel with HTTPS, rather than opening a port on the router.
- Cameras and microphones in a shop are regulated in many places (signs, consent, how long recordings may be kept). Check the rules that apply to you.

## When something looks wrong

- **A tile has the wrong shape, and the start-up log says "assuming 16:9":** the camera was offline or slow to answer when camwall started. Restart camwall once it's back, or add `aspect=4:3` (or the right shape) to its line.
- **A camera shows "Reconnecting…":** camwall keeps retrying on its own. Check that the address plays in VLC, and try `transport=udp` for older cameras.
- **The wall is slow or the computer is busy:** use each camera's sub-stream (`camwall discover -sub`), or lower `-fps` and `-max`.
- **Other devices can't open the wall:** start with `-addr :7788`, and allow camwall through the computer's firewall.
- **A web video doesn't play:** update yt-dlp (`brew upgrade yt-dlp` or `pip install -U yt-dlp`); sites change often.
- **`-labels` fails:** your FFmpeg lacks `drawtext`. Homebrew's FFmpeg has it.

## Limits

- Up to 25 feeds per wall. For more, run a second wall.
- Each feed costs one FFmpeg decode. A wall of 16 HD cameras needs a reasonably recent computer, so lower `-fps` or `-max` in the browser, or `-size` for streams, on small machines. A camera's sub-stream (often `…/102` or `subtype=1`) is lighter and plenty for a wall.
- The browser wall plays one camera's sound at a time, and the combined stream carries one feed's sound. Recording keeps every feed's sound.
- A capture device such as a webcam can usually be opened by only one program at a time, so it can't be in a browser wall and a combined stream at once.
- Animated WebP needs FFmpeg 8 for the combined stream. Older FFmpeg leaves it blank there; browsers still play it.
- DRM-protected video can't be shown.

## License

MIT. The pictures in this README come from freely licensed videos; see [docs/CREDITS.md](docs/CREDITS.md).
