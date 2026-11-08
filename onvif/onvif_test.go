package onvif

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeCamera answers the three SOAP calls a real ONVIF camera does, and
// checks the WS-Security digest the way cameras do.
func fakeCamera(t *testing.T, user, pass string) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var env struct {
			User    string `xml:"Header>Security>UsernameToken>Username"`
			Pass    string `xml:"Header>Security>UsernameToken>Password"`
			Nonce   string `xml:"Header>Security>UsernameToken>Nonce"`
			Created string `xml:"Header>Security>UsernameToken>Created"`
		}
		if err := xml.Unmarshal(b, &env); err != nil {
			t.Errorf("bad request XML: %v", err)
		}
		nonce, _ := base64.StdEncoding.DecodeString(env.Nonce)
		h := sha1.New()
		h.Write(nonce)
		h.Write([]byte(env.Created))
		h.Write([]byte(pass))
		if env.User != user || env.Pass != base64.StdEncoding.EncodeToString(h.Sum(nil)) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:ter="http://www.onvif.org/ver10/error"><s:Body><s:Fault><s:Code><s:Value>s:Sender</s:Value><s:Subcode><s:Value>ter:NotAuthorized</s:Value></s:Subcode></s:Code><s:Reason><s:Text>Sender not Authorized</s:Text></s:Reason></s:Fault></s:Body></s:Envelope>`)
			return
		}
		host := strings.TrimPrefix(srv.URL, "http://")
		_, port, _ := net.SplitHostPort(host)
		body := string(b)
		w.Header().Set("Content-Type", "application/soap+xml")
		switch {
		case strings.Contains(body, "GetCapabilities"):
			// a camera behind NAT reporting its own loopback address
			fmt.Fprintf(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema"><s:Body><tds:GetCapabilitiesResponse><tds:Capabilities><tt:Media><tt:XAddr>http://0.0.0.0:%s/onvif/Media</tt:XAddr></tt:Media></tds:Capabilities></tds:GetCapabilitiesResponse></s:Body></s:Envelope>`, port)
		case strings.Contains(body, "GetProfiles"):
			if r.URL.Path != "/onvif/Media" {
				t.Errorf("GetProfiles sent to %s", r.URL.Path)
			}
			fmt.Fprint(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema"><s:Body><trt:GetProfilesResponse>
<trt:Profiles token="sub" fixed="true"><tt:Name>SubStream</tt:Name><tt:VideoEncoderConfiguration><tt:Resolution><tt:Width>640</tt:Width><tt:Height>360</tt:Height></tt:Resolution></tt:VideoEncoderConfiguration></trt:Profiles>
<trt:Profiles token="main" fixed="true"><tt:Name>MainStream</tt:Name><tt:VideoEncoderConfiguration><tt:Resolution><tt:Width>1920</tt:Width><tt:Height>1080</tt:Height></tt:Resolution></tt:VideoEncoderConfiguration></trt:Profiles>
</trt:GetProfilesResponse></s:Body></s:Envelope>`)
		case strings.Contains(body, "GetStreamUri"):
			tok := "main"
			if strings.Contains(body, ">sub<") {
				tok = "sub"
			}
			fmt.Fprintf(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema"><s:Body><trt:GetStreamUriResponse><trt:MediaUri><tt:Uri>rtsp://127.0.0.1:554/Streaming/%s</tt:Uri></trt:MediaUri></trt:GetStreamUriResponse></s:Body></s:Envelope>`, tok)
		default:
			t.Errorf("unexpected call: %s", body)
		}
	}))
	return srv
}

func TestStreams(t *testing.T) {
	cam := fakeCamera(t, "admin", "s3cret pass")
	defer cam.Close()
	c := &Client{User: "admin", Password: "s3cret pass"}
	ss, err := c.Streams(context.Background(), cam.URL+"/onvif/device_service")
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 || ss[0].Profile != "MainStream" || ss[0].Width != 1920 {
		t.Fatalf("streams %+v", ss)
	}
	if ss[0].URI != "rtsp://admin:s3cret%20pass@127.0.0.1:554/Streaming/main" {
		t.Errorf("main stream URI %q", ss[0].URI)
	}
	if !strings.HasSuffix(ss[1].URI, "/Streaming/sub") {
		t.Errorf("sub stream URI %q", ss[1].URI)
	}
	bad := &Client{User: "admin", Password: "wrong"}
	if _, err := bad.Streams(context.Background(), cam.URL+"/onvif/device_service"); err != ErrAuth {
		t.Errorf("wrong password: %v", err)
	}
}

func TestDiscover(t *testing.T) {
	// a fake camera answering probes on localhost (multicast is not
	// available everywhere tests run)
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skip("no UDP:", err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 8192)
		for {
			n, from, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if !strings.Contains(string(buf[:n]), "NetworkVideoTransmitter") {
				continue
			}
			pc.WriteToUDP([]byte(`<?xml version="1.0"?><SOAP-ENV:Envelope xmlns:SOAP-ENV="http://www.w3.org/2003/05/soap-envelope" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery"><SOAP-ENV:Body><d:ProbeMatches><d:ProbeMatch>
<d:Scopes>onvif://www.onvif.org/type/video_encoder onvif://www.onvif.org/name/Front%20Door onvif://www.onvif.org/hardware/DS-2CD2143</d:Scopes>
<d:XAddrs>http://169.254.1.5/onvif/device_service http://127.0.0.1/onvif/device_service</d:XAddrs>
</d:ProbeMatch></d:ProbeMatches></SOAP-ENV:Body></SOAP-ENV:Envelope>`), from)
		}
	}()
	ds, err := Discover(context.Background(), pc.LocalAddr().String(), 700*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 {
		t.Fatalf("found %d devices (repeated answers should be merged)", len(ds))
	}
	d := ds[0]
	if d.Name != "Front Door" || d.Model != "DS-2CD2143" || d.XAddr != "http://127.0.0.1/onvif/device_service" {
		t.Errorf("device %+v", d)
	}
}
