package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func freeUDPPort(t *testing.T) int {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

func TestLocalIPDoesNotPanic(t *testing.T) {
	t.Log("primary:", primaryLocalIPv4())
	tg, self := lanScanTargets()
	t.Log("targets:", len(tg), "self:", self)
	if len(tg) < 1 || tg[0] != "255.255.255.255" {
		t.Fatal("alvos inválidos")
	}
	for _, c := range []struct {
		ip string
		p  bool
	}{{"192.168.0.5", true}, {"10.1.2.3", true}, {"172.20.1.1", true}, {"100.70.0.1", true}, {"8.8.8.8", false}, {"172.32.0.1", false}} {
		if isPrivateIPv4Str(c.ip) != c.p {
			t.Fatal("privado errado", c.ip)
		}
	}
}

func TestPublicIP(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>oops</html>") }))
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "203.0.113.7\n") }))
	defer bad.Close()
	defer good.Close()
	p := &publicIPLookup{state: "idle"}
	p.Start([]string{bad.URL, good.URL})
	if s, _ := p.Status(); s != "pending" {
		t.Fatal("esperava pending, veio", s)
	}
	for i := 0; i < 50; i++ {
		if s, ip := p.Status(); s == "ok" {
			if ip != "203.0.113.7" {
				t.Fatal("ip errado", ip)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timeout")
}

func TestPublicIPFail(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>oops</html>") }))
	defer bad.Close()
	p := &publicIPLookup{state: "idle"}
	p.Start([]string{bad.URL})
	for i := 0; i < 50; i++ {
		if s, _ := p.Status(); s == "fail" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("deveria falhar")
}

// ---- roteador falso ----
type fakeIGD struct {
	mu       sync.Mutex
	adds     []string
	dels     []string
	rejectLease bool // responde 725 para lease != 0
	deny     bool
	srv      *httptest.Server
	ssdp     *net.UDPConn
}

func newFakeIGD(t *testing.T) *fakeIGD {
	f := &fakeIGD{}
	mux := http.NewServeMux()
	mux.HandleFunc("/desc.xml", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0"><device><deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:1</deviceType><deviceList><device><deviceType>urn:schemas-upnp-org:device:WANDevice:1</deviceType><deviceList><device><deviceType>urn:schemas-upnp-org:device:WANConnectionDevice:1</deviceType><serviceList><service><serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType><controlURL>/ctl/IPConn</controlURL></service></serviceList></device></deviceList></device></deviceList></device></root>`)
	})
	mux.HandleFunc("/ctl/IPConn", func(w http.ResponseWriter, r *http.Request) {
		act := r.Header.Get("SOAPAction")
		buf := new(strings.Builder)
		b := make([]byte, 4096)
		for {
			n, err := r.Body.Read(b)
			buf.Write(b[:n])
			if err != nil {
				break
			}
		}
		body := buf.String()
		fault := func(code int) {
			w.WriteHeader(500)
			fmt.Fprintf(w, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><detail><UPnPError><errorCode>%d</errorCode><errorDescription>x</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`, code)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case strings.Contains(act, "GetExternalIPAddress"):
			fmt.Fprint(w, `<s:Envelope><s:Body><u:GetExternalIPAddressResponse><NewExternalIPAddress>198.51.100.9</NewExternalIPAddress></u:GetExternalIPAddressResponse></s:Body></s:Envelope>`)
		case strings.Contains(act, "AddPortMapping"):
			if f.deny {
				fault(606)
				return
			}
			if f.rejectLease && !strings.Contains(body, "<NewLeaseDuration>0</NewLeaseDuration>") {
				fault(725)
				return
			}
			proto := "?"
			if strings.Contains(body, "<NewProtocol>TCP</NewProtocol>") {
				proto = "TCP"
			} else if strings.Contains(body, "<NewProtocol>UDP</NewProtocol>") {
				proto = "UDP"
			}
			port := ""
			if i := strings.Index(body, "<NewExternalPort>"); i >= 0 {
				port = body[i+17 : i+17+strings.Index(body[i+17:], "<")]
			}
			f.adds = append(f.adds, proto+":"+port)
			fmt.Fprint(w, `<s:Envelope><s:Body><u:AddPortMappingResponse/></s:Body></s:Envelope>`)
		case strings.Contains(act, "DeletePortMapping"):
			proto := "?"
			if strings.Contains(body, "<NewProtocol>TCP</NewProtocol>") {
				proto = "TCP"
			} else if strings.Contains(body, "<NewProtocol>UDP</NewProtocol>") {
				proto = "UDP"
			}
			f.dels = append(f.dels, proto)
			fmt.Fprint(w, `<s:Envelope><s:Body><u:DeletePortMappingResponse/></s:Body></s:Envelope>`)
		default:
			fault(401)
		}
	})
	f.srv = httptest.NewServer(mux)
	// SSDP falso em UDP unicast local
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	f.ssdp = c
	go func() {
		buf := make([]byte, 1024)
		for {
			n, from, err := c.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if strings.Contains(string(buf[:n]), "M-SEARCH") && strings.Contains(string(buf[:n]), "InternetGatewayDevice:1") {
				c.WriteToUDP([]byte("HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=120\r\nLocation: "+f.srv.URL+"/desc.xml\r\nST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\n\r\n"), from)
			}
		}
	}()
	return f
}
func (f *fakeIGD) close() { f.srv.Close(); f.ssdp.Close() }

func waitState(t *testing.T, m *upnpManager, want string) (string, string, string) {
	for i := 0; i < 100; i++ {
		s, e, msg := m.Status()
		if s == want {
			return s, e, msg
		}
		time.Sleep(50 * time.Millisecond)
	}
	s, e, msg := m.Status()
	t.Fatalf("estado %q (esperava %q) msg=%q", s, want, msg)
	return s, e, msg
}

func withFake(t *testing.T, f *fakeIGD, fn func(m *upnpManager)) {
	oldAddr, oldT := ssdpAddr, upnpSearchTime
	ssdpAddr = f.ssdp.LocalAddr().String()
	upnpSearchTime = 1500 * time.Millisecond
	defer func() { ssdpAddr, upnpSearchTime = oldAddr, oldT }()
	fn(&upnpManager{state: "idle"})
}

func TestUPnPOpenAndClose(t *testing.T) {
	f := newFakeIGD(t)
	defer f.close()
	withFake(t, f, func(m *upnpManager) {
		m.Start(7500, 7502)
		_, ext, _ := waitState(t, m, "ok")
		if ext != "198.51.100.9" {
			t.Fatal("ext", ext)
		}
		f.mu.Lock()
		got := strings.Join(f.adds, ",")
		f.mu.Unlock()
		if got != "TCP:7500,UDP:7502" {
			t.Fatal("mapeamentos:", got)
		}
		m.Close()
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.dels) != 2 {
			t.Fatal("deletes:", f.dels)
		}
		if s, _, _ := m.Status(); s != "idle" {
			t.Fatal("deveria voltar a idle")
		}
	})
}

func TestUPnPRetryPermanentLease(t *testing.T) {
	f := newFakeIGD(t)
	f.rejectLease = true
	defer f.close()
	withFake(t, f, func(m *upnpManager) {
		m.Start(7500, 0)
		waitState(t, m, "ok")
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.adds) != 1 || f.adds[0] != "TCP:7500" {
			t.Fatal("adds:", f.adds)
		}
		m.Close()
	})
}

func TestUPnPDenied(t *testing.T) {
	f := newFakeIGD(t)
	f.deny = true
	defer f.close()
	withFake(t, f, func(m *upnpManager) {
		m.Start(7500, 7500)
		_, _, msg := waitState(t, m, "fail")
		if msg != "denied" {
			t.Fatal("msg", msg)
		}
	})
}

func TestUPnPNoGateway(t *testing.T) {
	oldAddr, oldT := ssdpAddr, upnpSearchTime
	ssdpAddr, upnpSearchTime = "127.0.0.1:9", 400*time.Millisecond // porta sem ninguém
	defer func() { ssdpAddr, upnpSearchTime = oldAddr, oldT }()
	m := &upnpManager{state: "idle"}
	m.Start(7500, 7500)
	_, _, msg := waitState(t, m, "fail")
	if msg != "nogateway" {
		t.Fatal("msg", msg)
	}
}

func TestLanDiscovery(t *testing.T) {
	port := freeUDPPort(t)
	h := &lanHost{}
	if err := h.Start(lanInfo{Name: "Sala do João", Port: 7500, Rollback: true, Ver: "1.0"}, port); err != nil {
		t.Fatal(err)
	}
	defer h.Stop()
	s := &lanScanner{state: "idle"}
	s.Start([]string{"127.0.0.1"}, map[string]bool{}, port, 1500*time.Millisecond)
	var rooms []lanRoom
	for i := 0; i < 60; i++ {
		_, rooms = s.Results()
		if len(rooms) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(rooms) != 1 {
		t.Fatal("salas:", rooms)
	}
	r := rooms[0]
	if r.Name != "Sala do João" || r.Port != 7500 || !r.Rollback || r.IP != "127.0.0.1" || r.Ver != "1.0" {
		t.Fatalf("sala errada: %+v", r)
	}
	// reenvio na 2ª rodada não duplica
	time.Sleep(1000 * time.Millisecond)
	if _, rooms = s.Results(); len(rooms) != 1 {
		t.Fatal("duplicou:", rooms)
	}
	for i := 0; i < 60; i++ {
		if st, _ := s.Results(); st == "done" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st, _ := s.Results(); st != "done" {
		t.Fatal("estado final", st)
	}
	s.Stop()
	if st, _ := s.Results(); st != "idle" {
		t.Fatal("stop")
	}
}

func TestLanIgnoresGarbage(t *testing.T) {
	port := freeUDPPort(t)
	g, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	defer g.Close()
	go func() {
		buf := make([]byte, 100)
		for {
			_, from, err := g.ReadFromUDP(buf)
			if err != nil {
				return
			}
			g.WriteToUDP([]byte(`{"magic":"OUTRO","port":1}`), from)
			g.WriteToUDP([]byte(`lixo`), from)
		}
	}()
	s := &lanScanner{state: "idle"}
	s.Start([]string{"127.0.0.1"}, map[string]bool{}, port, 700*time.Millisecond)
	time.Sleep(900 * time.Millisecond)
	if _, rooms := s.Results(); len(rooms) != 0 {
		t.Fatal("aceitou lixo", rooms)
	}
}
