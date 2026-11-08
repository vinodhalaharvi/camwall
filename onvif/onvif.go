// Package onvif finds ONVIF cameras on the local network and asks them for
// their RTSP stream addresses, so nobody has to dig them out of a manual.
//
// Discovery is WS-Discovery: one multicast probe, and every camera that
// answers names its device service. The device service gives the media
// service, which lists the camera's profiles (main stream, sub-stream …) and
// the RTSP address of each. Requests are signed with a WS-Security
// UsernameToken digest, which is what ONVIF cameras expect.
package onvif

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Device is a camera that answered the discovery probe.
type Device struct {
	Addr  string   // IP address it answered from
	XAddr string   // device service URL
	Name  string   // from its scopes, when it gives one
	Model string   // hardware scope
	All   []string // every device service URL it gave
}

// Stream is one of a camera's profiles and where to watch it.
type Stream struct {
	Profile string
	Width   int
	Height  int
	URI     string // RTSP address, with the credentials filled in
}

// MulticastAddr is where WS-Discovery probes go.
const MulticastAddr = "239.255.255.250:3702"

const probeTmpl = `<?xml version="1.0" encoding="UTF-8"?>
<e:Envelope xmlns:e="http://www.w3.org/2003/05/soap-envelope" xmlns:w="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:dn="http://www.onvif.org/ver10/network/wsdl">
<e:Header><w:MessageID>uuid:%s</w:MessageID><w:To e:mustUnderstand="true">urn:schemas-xmlsoap-org:ws:2005:04:discovery</w:To><w:Action e:mustUnderstand="true">http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</w:Action></e:Header>
<e:Body><d:Probe><d:Types>dn:NetworkVideoTransmitter</d:Types></d:Probe></e:Body></e:Envelope>`

func uuid() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

type probeMatches struct {
	Matches []struct {
		XAddrs string `xml:"XAddrs"`
		Scopes string `xml:"Scopes"`
	} `xml:"Body>ProbeMatches>ProbeMatch"`
}

// parseProbeMatch reads a ProbeMatches answer.
func parseProbeMatch(b []byte, from string) []Device {
	var pm probeMatches
	if xml.Unmarshal(b, &pm) != nil {
		return nil
	}
	var out []Device
	for _, m := range pm.Matches {
		addrs := strings.Fields(m.XAddrs)
		if len(addrs) == 0 {
			continue
		}
		d := Device{Addr: from, All: addrs, XAddr: addrs[0]}
		// prefer the address on the network it answered from (cameras often list several)
		for _, a := range addrs {
			if u, err := url.Parse(a); err == nil && u.Hostname() == from {
				d.XAddr = a
				break
			}
		}
		for _, s := range strings.Fields(m.Scopes) {
			if v, ok := strings.CutPrefix(s, "onvif://www.onvif.org/name/"); ok {
				d.Name, _ = url.PathUnescape(v)
			}
			if v, ok := strings.CutPrefix(s, "onvif://www.onvif.org/hardware/"); ok {
				d.Model, _ = url.PathUnescape(v)
			}
		}
		out = append(out, d)
	}
	return out
}

// Discover sends a probe to target (MulticastAddr normally) and collects
// answers until the timeout. Each camera is listed once.
func Discover(ctx context.Context, target string, wait time.Duration) ([]Device, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	dst, err := net.ResolveUDPAddr("udp4", target)
	if err != nil {
		return nil, err
	}
	msg := []byte(fmt.Sprintf(probeTmpl, uuid()))
	for i := 0; i < 2; i++ { // UDP can drop; cameras ignore repeats of the same probe id … close enough
		if _, err := conn.WriteToUDP(msg, dst); err != nil {
			return nil, fmt.Errorf("sending the discovery probe: %w", err)
		}
	}
	deadline := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	seen := map[string]bool{}
	var out []Device
	buf := make([]byte, 64<<10)
	for {
		conn.SetReadDeadline(deadline)
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				break
			}
			return out, err
		}
		for _, d := range parseProbeMatch(buf[:n], from.IP.String()) {
			if !seen[d.XAddr] {
				seen[d.XAddr] = true
				out = append(out, d)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Addr < out[j].Addr })
	return out, nil
}

// ---------- SOAP ----------

// security is a WS-Security UsernameToken with a password digest:
// Base64(SHA-1(nonce + created + password)).
func security(user, pass string, now time.Time) string {
	if user == "" {
		return ""
	}
	var nonce [16]byte
	rand.Read(nonce[:])
	created := now.UTC().Format("2006-01-02T15:04:05.000Z")
	h := sha1.New()
	h.Write(nonce[:])
	h.Write([]byte(created))
	h.Write([]byte(pass))
	return `<s:Header><Security xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd" s:mustUnderstand="1"><UsernameToken>` +
		`<Username>` + xmlEscape(user) + `</Username>` +
		`<Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">` + base64.StdEncoding.EncodeToString(h.Sum(nil)) + `</Password>` +
		`<Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">` + base64.StdEncoding.EncodeToString(nonce[:]) + `</Nonce>` +
		`<Created xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">` + created + `</Created>` +
		`</UsernameToken></Security></s:Header>`
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Client talks to one camera.
type Client struct {
	HTTP     *http.Client
	User     string
	Password string
	Now      func() time.Time // for tests; time.Now by default
}

// ErrAuth means the camera refused the user name or password.
var ErrAuth = errors.New("the camera refused the user name or password")

type fault struct {
	Reason string `xml:"Body>Fault>Reason>Text"`
	Code   string `xml:"Body>Fault>Code>Subcode>Value"`
}

func (c *Client) call(ctx context.Context, endpoint, body string, out any) error {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	env := `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" ` +
		`xmlns:tds="http://www.onvif.org/ver10/device/wsdl" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">` +
		security(c.User, c.Password, now()) + `<s:Body>` + body + `</s:Body></s:Envelope>`
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(env))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", `application/soap+xml; charset=utf-8`)
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 8 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var f fault
		xml.Unmarshal(b, &f)
		if resp.StatusCode == http.StatusUnauthorized || strings.Contains(f.Code, "NotAuthorized") || strings.Contains(strings.ToLower(f.Reason), "auth") {
			return ErrAuth
		}
		if f.Reason != "" {
			return fmt.Errorf("camera says: %s", strings.TrimSpace(f.Reason))
		}
		return fmt.Errorf("camera answered %s", resp.Status)
	}
	return xml.Unmarshal(b, out)
}

type capabilities struct {
	Media string `xml:"Body>GetCapabilitiesResponse>Capabilities>Media>XAddr"`
}

type profiles struct {
	Profiles []struct {
		Token string `xml:"token,attr"`
		Name  string `xml:"Name"`
		Width int    `xml:"VideoEncoderConfiguration>Resolution>Width"`
		Hgt   int    `xml:"VideoEncoderConfiguration>Resolution>Height"`
	} `xml:"Body>GetProfilesResponse>Profiles"`
}

type streamURI struct {
	URI string `xml:"Body>GetStreamUriResponse>MediaUri>Uri"`
}

// Streams lists a camera's profiles and their RTSP addresses, main stream
// first. The returned addresses carry the client's credentials, ready to use.
func (c *Client) Streams(ctx context.Context, deviceXAddr string) ([]Stream, error) {
	media := deviceXAddr
	var caps capabilities
	if err := c.call(ctx, deviceXAddr, `<tds:GetCapabilities><tds:Category>Media</tds:Category></tds:GetCapabilities>`, &caps); err != nil {
		if err == ErrAuth {
			return nil, err
		}
	} else if caps.Media != "" {
		media = sameHost(caps.Media, deviceXAddr)
	}
	var ps profiles
	if err := c.call(ctx, media, `<trt:GetProfiles/>`, &ps); err != nil {
		return nil, err
	}
	if len(ps.Profiles) == 0 {
		return nil, errors.New("the camera lists no video profiles")
	}
	var out []Stream
	for _, p := range ps.Profiles {
		var su streamURI
		body := `<trt:GetStreamUri><trt:StreamSetup><tt:Stream>RTP-Unicast</tt:Stream><tt:Transport><tt:Protocol>RTSP</tt:Protocol></tt:Transport></trt:StreamSetup>` +
			`<trt:ProfileToken>` + xmlEscape(p.Token) + `</trt:ProfileToken></trt:GetStreamUri>`
		if err := c.call(ctx, media, body, &su); err != nil {
			return out, err
		}
		if su.URI == "" {
			continue
		}
		name := p.Name
		if name == "" {
			name = p.Token
		}
		out = append(out, Stream{Profile: name, Width: p.Width, Height: p.Hgt, URI: withCredentials(sameHost(su.URI, deviceXAddr), c.User, c.Password)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Width*out[i].Height > out[j].Width*out[j].Height })
	return out, nil
}

// sameHost replaces an address the camera reports for itself when it is a
// loopback or unspecified one (some cameras do that behind NAT) with the host
// we reached it on.
func sameHost(addr, reached string) string {
	u, err := url.Parse(addr)
	r, err2 := url.Parse(reached)
	if err != nil || err2 != nil {
		return addr
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		if p := u.Port(); p != "" {
			u.Host = net.JoinHostPort(r.Hostname(), p)
		} else {
			u.Host = r.Hostname()
		}
	}
	return u.String()
}

func withCredentials(rtsp, user, pass string) string {
	if user == "" {
		return rtsp
	}
	u, err := url.Parse(rtsp)
	if err != nil || u.User != nil {
		return rtsp
	}
	u.User = url.UserPassword(user, pass)
	return u.String()
}
