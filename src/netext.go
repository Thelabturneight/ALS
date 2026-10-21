package main

// Extensões de rede para o modo Online (sem dependências do resto do motor):
//   - descoberta de salas na rede local (UDP);
//   - abertura automática de portas no roteador (UPnP IGD);
//   - consulta do IP público;
//   - utilidades de IP local.
// As funções Lua que expõem isso ficam em netext_lua.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	lanDiscoveryPort = 7501
	lanMagic         = "IKEMENGO-LAN"
	lanProbe         = "IKEMENGO-LAN?1"
)

// ---------------------------------------------------------------------------
// IP local
// ---------------------------------------------------------------------------

func isUsableIPv4(ip net.IP) bool {
	ip4 := ip.To4()
	return ip4 != nil && !ip4.IsLoopback() && !ip4.IsUnspecified() && !ip4.IsLinkLocalUnicast() && !ip4.IsMulticast()
}

// primaryLocalIPv4 devolve o IPv4 usado para sair para a internet/rede. Não envia pacotes
// (UDP "connect" só escolhe a rota) e funciona no Android 11+, onde net.Interfaces pode falhar.
func primaryLocalIPv4() string {
	for _, target := range []string{"8.8.8.8:53", "192.168.0.1:80", "10.0.0.1:80"} {
		c, err := net.Dial("udp4", target)
		if err != nil {
			continue
		}
		la, _ := c.LocalAddr().(*net.UDPAddr)
		c.Close()
		if la != nil && isUsableIPv4(la.IP) {
			return la.IP.String()
		}
	}
	for _, n := range localIPv4Nets() {
		return n.IP.String()
	}
	return ""
}

// localIPv4Nets lista as redes IPv4 locais (pode vir vazia se o sistema negar a consulta).
func localIPv4Nets() []*net.IPNet {
	var out []*net.IPNet
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && isUsableIPv4(ipn.IP) {
			out = append(out, &net.IPNet{IP: ipn.IP.To4(), Mask: ipn.Mask})
		}
	}
	return out
}

func isPrivateIPv4Str(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() == nil {
		return false
	}
	ip4 := ip.To4()
	switch {
	case ip4[0] == 10:
		return true
	case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
		return true
	case ip4[0] == 192 && ip4[1] == 168:
		return true
	case ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127: // CGNAT
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// IP público
// ---------------------------------------------------------------------------

var publicIPURLs = []string{
	"https://api.ipify.org",
	"https://ipv4.icanhazip.com",
	"https://checkip.amazonaws.com",
}

type publicIPLookup struct {
	mu    sync.Mutex
	state string // idle | pending | ok | fail
	ip    string
	gen   int
}

var pubIP = &publicIPLookup{state: "idle"}

func (p *publicIPLookup) Start(urls []string) {
	p.mu.Lock()
	if p.state == "pending" {
		p.mu.Unlock()
		return
	}
	p.state, p.ip = "pending", ""
	p.gen++
	gen := p.gen
	p.mu.Unlock()
	go func() {
		client := &http.Client{Timeout: 4 * time.Second}
		ip := ""
		for _, u := range urls {
			resp, err := client.Get(u)
			if err != nil {
				continue
			}
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
			resp.Body.Close()
			s := strings.TrimSpace(string(b))
			if pi := net.ParseIP(s); pi != nil && pi.To4() != nil && resp.StatusCode == 200 {
				ip = pi.To4().String()
				break
			}
		}
		p.mu.Lock()
		if p.gen == gen {
			if ip != "" {
				p.state, p.ip = "ok", ip
			} else {
				p.state = "fail"
			}
		}
		p.mu.Unlock()
	}()
}

func (p *publicIPLookup) Status() (string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state, p.ip
}

// ---------------------------------------------------------------------------
// UPnP (IGD)
// ---------------------------------------------------------------------------

type upnpService struct {
	ServiceType string `xml:"serviceType"`
	ControlURL  string `xml:"controlURL"`
}
type upnpDevice struct {
	Services []upnpService `xml:"serviceList>service"`
	Devices  []upnpDevice  `xml:"deviceList>device"`
}
type upnpDesc struct {
	URLBase string     `xml:"URLBase"`
	Device  upnpDevice `xml:"device"`
}

type upnpGateway struct {
	controlURL  string
	serviceType string
	localIP     string // IP local usado para falar com o roteador
}

type upnpMapping struct {
	port     int
	protocol string
}

type upnpManager struct {
	mu      sync.Mutex
	state   string // idle | search | ok | fail
	msg     string // nogateway | denied | error:<texto>
	extIP   string
	gw      *upnpGateway
	mapped  []upnpMapping
	gen     int
	stopRen chan struct{}
}

var upnp = &upnpManager{state: "idle"}

var (
	ssdpAddr        = "239.255.255.250:1900"
	upnpSearchTime  = 3 * time.Second
	upnpLeaseSecs   = 7200
	upnpRenewEvery  = 30 * time.Minute
	upnpHTTPTimeout = 4 * time.Second
)

func upnpSearchTargets() []string {
	return []string{
		"urn:schemas-upnp-org:device:InternetGatewayDevice:1",
		"urn:schemas-upnp-org:device:InternetGatewayDevice:2",
		"urn:schemas-upnp-org:service:WANIPConnection:1",
		"urn:schemas-upnp-org:service:WANPPPConnection:1",
	}
}

// upnpDiscoverLocations manda M-SEARCH e devolve as URLs de descrição que responderam.
func upnpDiscoverLocations(addr string, wait time.Duration) []string {
	raddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil
	}
	defer conn.Close()
	for round := 0; round < 2; round++ {
		for _, st := range upnpSearchTargets() {
			msg := "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\nST: " + st + "\r\n\r\n"
			conn.WriteToUDP([]byte(msg), raddr)
		}
		if round == 0 {
			time.Sleep(150 * time.Millisecond)
		}
	}
	seen := map[string]bool{}
	var locs []string
	deadline := time.Now().Add(wait)
	buf := make([]byte, 2048)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		for _, line := range strings.Split(string(buf[:n]), "\r\n") {
			if len(line) > 9 && strings.EqualFold(line[:9], "location:") {
				l := strings.TrimSpace(line[9:])
				if l != "" && !seen[l] {
					seen[l] = true
					locs = append(locs, l)
				}
			}
		}
		if len(locs) > 0 && time.Until(deadline) > 600*time.Millisecond {
			deadline = time.Now().Add(600 * time.Millisecond) // já achou: só espera mais um pouco por outros
		}
	}
	return locs
}

func findWANService(d upnpDevice) *upnpService {
	var best *upnpService
	for i := range d.Services {
		s := &d.Services[i]
		if strings.Contains(s.ServiceType, "WANIPConnection") {
			return s
		}
		if strings.Contains(s.ServiceType, "WANPPPConnection") && best == nil {
			best = s
		}
	}
	if best != nil {
		return best
	}
	for _, sub := range d.Devices {
		if s := findWANService(sub); s != nil {
			if strings.Contains(s.ServiceType, "WANIPConnection") {
				return s
			}
			if best == nil {
				best = s
			}
		}
	}
	return best
}

func upnpLoadGateway(location string) (*upnpGateway, error) {
	client := &http.Client{Timeout: upnpHTTPTimeout}
	resp, err := client.Get(location)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var desc upnpDesc
	if err := xml.Unmarshal(body, &desc); err != nil {
		return nil, err
	}
	svc := findWANService(desc.Device)
	if svc == nil {
		return nil, fmt.Errorf("sem serviço WAN")
	}
	base, err := url.Parse(location)
	if err != nil {
		return nil, err
	}
	if desc.URLBase != "" {
		if ub, err := url.Parse(desc.URLBase); err == nil {
			base = ub
		}
	}
	cu, err := base.Parse(svc.ControlURL)
	if err != nil {
		return nil, err
	}
	// IP local que o roteador enxerga
	host := cu.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	c, err := net.DialTimeout("tcp4", host, upnpHTTPTimeout)
	if err != nil {
		return nil, err
	}
	lip := ""
	if ta, ok := c.LocalAddr().(*net.TCPAddr); ok {
		lip = ta.IP.String()
	}
	c.Close()
	if lip == "" {
		return nil, fmt.Errorf("sem IP local")
	}
	return &upnpGateway{controlURL: cu.String(), serviceType: svc.ServiceType, localIP: lip}, nil
}

type upnpFault struct {
	code int
	desc string
}

func (f *upnpFault) Error() string { return fmt.Sprintf("UPnP %d %s", f.code, f.desc) }

var (
	reFaultCode = regexp.MustCompile(`<errorCode>\s*(\d+)\s*</errorCode>`)
	reFaultDesc = regexp.MustCompile(`<errorDescription>([^<]*)</errorDescription>`)
	reExtIP     = regexp.MustCompile(`<NewExternalIPAddress>([^<]*)</NewExternalIPAddress>`)
)

func (g *upnpGateway) soap(action string, args [][2]string) (string, error) {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>`)
	fmt.Fprintf(&b, `<u:%s xmlns:u="%s">`, action, g.serviceType)
	for _, a := range args {
		fmt.Fprintf(&b, "<%s>", a[0])
		xml.EscapeText(&b, []byte(a[1]))
		fmt.Fprintf(&b, "</%s>", a[0])
	}
	fmt.Fprintf(&b, `</u:%s></s:Body></s:Envelope>`, action)
	ctx, cancel := context.WithTimeout(context.Background(), upnpHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", g.controlURL, &b)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", `"`+g.serviceType+"#"+action+`"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		f := &upnpFault{code: resp.StatusCode}
		if m := reFaultCode.FindSubmatch(body); m != nil {
			f.code, _ = strconv.Atoi(string(m[1]))
		}
		if m := reFaultDesc.FindSubmatch(body); m != nil {
			f.desc = string(m[1])
		}
		return "", f
	}
	return string(body), nil
}

func (g *upnpGateway) externalIP() (string, error) {
	body, err := g.soap("GetExternalIPAddress", nil)
	if err != nil {
		return "", err
	}
	if m := reExtIP.FindStringSubmatch(body); m != nil {
		return strings.TrimSpace(m[1]), nil
	}
	return "", fmt.Errorf("resposta sem IP")
}

func (g *upnpGateway) addMapping(port int, proto string, lease int) error {
	args := [][2]string{
		{"NewRemoteHost", ""},
		{"NewExternalPort", strconv.Itoa(port)},
		{"NewProtocol", proto},
		{"NewInternalPort", strconv.Itoa(port)},
		{"NewInternalClient", g.localIP},
		{"NewEnabled", "1"},
		{"NewPortMappingDescription", "Anime Starlab"},
		{"NewLeaseDuration", strconv.Itoa(lease)},
	}
	_, err := g.soap("AddPortMapping", args)
	return err
}

// addMappingRetry trata roteadores que só aceitam mapeamento permanente (erro 725)
func (g *upnpGateway) addMappingRetry(port int, proto string) error {
	err := g.addMapping(port, proto, upnpLeaseSecs)
	if f, ok := err.(*upnpFault); ok && (f.code == 725 || f.code == 402 || f.code == 600) {
		return g.addMapping(port, proto, 0)
	}
	return err
}

func (g *upnpGateway) deleteMapping(port int, proto string) error {
	_, err := g.soap("DeletePortMapping", [][2]string{
		{"NewRemoteHost", ""},
		{"NewExternalPort", strconv.Itoa(port)},
		{"NewProtocol", proto},
	})
	return err
}

func (m *upnpManager) set(gen int, state, msg string) {
	m.mu.Lock()
	if m.gen == gen {
		m.state, m.msg = state, msg
	}
	m.mu.Unlock()
}

// Start procura o roteador e abre as portas (TCP para o jogo, UDP para o rollback), sem bloquear.
func (m *upnpManager) Start(tcpPort, udpPort int) {
	m.Close()
	m.mu.Lock()
	m.gen++
	gen := m.gen
	m.state, m.msg, m.extIP = "search", "", ""
	m.mu.Unlock()
	go func() {
		locs := upnpDiscoverLocations(ssdpAddr, upnpSearchTime)
		var gw *upnpGateway
		for _, l := range locs {
			if g, err := upnpLoadGateway(l); err == nil {
				gw = g
				break
			}
		}
		if gw == nil {
			m.set(gen, "fail", "nogateway")
			return
		}
		want := []upnpMapping{{tcpPort, "TCP"}}
		if udpPort > 0 {
			want = append(want, upnpMapping{udpPort, "UDP"})
		}
		var mapped []upnpMapping
		for _, w := range want {
			if err := gw.addMappingRetry(w.port, w.protocol); err != nil {
				for _, d := range mapped {
					gw.deleteMapping(d.port, d.protocol)
				}
				msg := "error:" + err.Error()
				if f, ok := err.(*upnpFault); ok && (f.code == 606 || f.code == 401 || f.code == 403) {
					msg = "denied"
				}
				m.set(gen, "fail", msg)
				return
			}
			mapped = append(mapped, w)
		}
		ext, _ := gw.externalIP()
		m.mu.Lock()
		if m.gen != gen { // cancelado enquanto trabalhava
			m.mu.Unlock()
			for _, d := range mapped {
				gw.deleteMapping(d.port, d.protocol)
			}
			return
		}
		m.gw, m.mapped, m.extIP = gw, mapped, ext
		m.state, m.msg = "ok", ""
		stop := make(chan struct{})
		m.stopRen = stop
		m.mu.Unlock()
		go func() {
			t := time.NewTicker(upnpRenewEvery)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					for _, w := range mapped {
						gw.addMappingRetry(w.port, w.protocol)
					}
				}
			}
		}()
	}()
}

func (m *upnpManager) Status() (state, ext, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state, m.extIP, m.msg
}

// Close fecha as portas abertas (se houver) e cancela buscas em andamento.
func (m *upnpManager) Close() {
	m.mu.Lock()
	m.gen++
	gw, mapped, stop := m.gw, m.mapped, m.stopRen
	m.gw, m.mapped, m.stopRen = nil, nil, nil
	m.state, m.msg, m.extIP = "idle", "", ""
	m.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if gw != nil {
		var wg sync.WaitGroup
		for _, d := range mapped {
			wg.Add(1)
			go func(d upnpMapping) { defer wg.Done(); gw.deleteMapping(d.port, d.protocol) }(d)
		}
		wg.Wait()
	}
}

// ---------------------------------------------------------------------------
// Descoberta de salas na rede local
// ---------------------------------------------------------------------------

type lanInfo struct {
	Magic    string `json:"magic"`
	Name     string `json:"name"`
	Port     int    `json:"port"`
	Rollback bool   `json:"rb"`
	Ver      string `json:"ver"`
}

type lanRoom struct {
	Name     string
	IP       string
	Port     int
	Rollback bool
	Ver      string
}

type lanHost struct {
	mu   sync.Mutex
	conn *net.UDPConn
	info lanInfo
}

var lanHosting = &lanHost{}

func (h *lanHost) Start(info lanInfo, discoveryPort int) error {
	h.Stop()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: discoveryPort})
	if err != nil {
		return err
	}
	info.Magic = lanMagic
	h.mu.Lock()
	h.conn, h.info = conn, info
	h.mu.Unlock()
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if !strings.HasPrefix(string(buf[:n]), "IKEMENGO-LAN?") {
				continue
			}
			h.mu.Lock()
			data, _ := json.Marshal(h.info)
			h.mu.Unlock()
			conn.WriteToUDP(data, from)
		}
	}()
	return nil
}

func (h *lanHost) Stop() {
	h.mu.Lock()
	c := h.conn
	h.conn = nil
	h.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// lanScanTargets: broadcast geral + broadcast dirigido + varredura /24 da rede local.
func lanScanTargets() (targets []string, self map[string]bool) {
	self = map[string]bool{}
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			targets = append(targets, s)
		}
	}
	add("255.255.255.255")
	nets := localIPv4Nets()
	if p := primaryLocalIPv4(); p != "" {
		self[p] = true
		found := false
		for _, n := range nets {
			if n.IP.String() == p {
				found = true
			}
		}
		if !found {
			nets = append(nets, &net.IPNet{IP: net.ParseIP(p).To4(), Mask: net.CIDRMask(24, 32)})
		}
	}
	for _, n := range nets {
		self[n.IP.String()] = true
		ones, bits := n.Mask.Size()
		if bits != 32 || ones == 32 {
			continue
		}
		if ones < 24 { // redes grandes: varre só o /24 em que estamos
			n = &net.IPNet{IP: n.IP, Mask: net.CIDRMask(24, 32)}
			ones = 24
		}
		base := n.IP.Mask(n.Mask).To4()
		bc := make(net.IP, 4)
		for i := 0; i < 4; i++ {
			bc[i] = base[i] | ^n.Mask[i]
		}
		add(bc.String())
		size := 1 << uint(32-ones)
		for i := 1; i < size-1; i++ {
			ip := make(net.IP, 4)
			copy(ip, base)
			v := (int(base[2])<<8 | int(base[3])) + i
			ip[2], ip[3] = byte(v>>8), byte(v&255)
			if !self[ip.String()] {
				add(ip.String())
			}
		}
	}
	return
}

type lanScanner struct {
	mu     sync.Mutex
	state  string // idle | scanning | done
	rooms  []lanRoom
	cancel chan struct{}
}

var lanScan = &lanScanner{state: "idle"}

func (s *lanScanner) Start(targets []string, self map[string]bool, discoveryPort int, dur time.Duration) {
	s.Stop()
	s.mu.Lock()
	cancel := make(chan struct{})
	s.cancel, s.state, s.rooms = cancel, "scanning", nil
	s.mu.Unlock()
	go func() {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
		if err != nil {
			s.mu.Lock()
			if s.cancel == cancel {
				s.state = "done"
			}
			s.mu.Unlock()
			return
		}
		defer conn.Close()
		done := make(chan struct{})
		go func() { // envio: 2 rodadas
			defer close(done)
			for round := 0; round < 2; round++ {
				for i, t := range targets {
					select {
					case <-cancel:
						return
					default:
					}
					conn.WriteToUDP([]byte(lanProbe), &net.UDPAddr{IP: net.ParseIP(t), Port: discoveryPort})
					if i%32 == 31 {
						time.Sleep(5 * time.Millisecond)
					}
				}
				time.Sleep(700 * time.Millisecond)
			}
		}()
		deadline := time.Now().Add(dur)
		buf := make([]byte, 1024)
		for time.Now().Before(deadline) {
			select {
			case <-cancel:
				return
			default:
			}
			conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				continue
			}
			var info lanInfo
			if json.Unmarshal(buf[:n], &info) != nil || info.Magic != lanMagic || info.Port <= 0 || info.Port > 65535 {
				continue
			}
			ip := from.IP.String()
			if self[ip] && !from.IP.IsLoopback() {
				continue // a própria sala
			}
			s.mu.Lock()
			if s.cancel == cancel {
				dup := false
				for _, r := range s.rooms {
					if r.IP == ip && r.Port == info.Port {
						dup = true
					}
				}
				if !dup {
					name := info.Name
					if len(name) > 24 {
						name = name[:24]
					}
					s.rooms = append(s.rooms, lanRoom{Name: name, IP: ip, Port: info.Port, Rollback: info.Rollback, Ver: info.Ver})
				}
			}
			s.mu.Unlock()
		}
		<-done
		s.mu.Lock()
		if s.cancel == cancel {
			s.state = "done"
		}
		s.mu.Unlock()
	}()
}

func (s *lanScanner) Results() (string, []lanRoom) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]lanRoom, len(s.rooms))
	copy(out, s.rooms)
	return s.state, out
}

func (s *lanScanner) Stop() {
	s.mu.Lock()
	if s.cancel != nil {
		close(s.cancel)
		s.cancel = nil
	}
	s.state = "idle"
	s.mu.Unlock()
}
