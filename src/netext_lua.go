package main

import (
	"time"

	lua "github.com/yuin/gopher-lua"
)

// netExtScriptInit registra as funções Lua do modo Online.
//
//	netLocalIP() -> string                          IPv4 local ("" se não houver rede)
//	netPublicIPStart()                              inicia a consulta do IP público (assíncrona)
//	netPublicIP() -> state, ip                      state: idle|pending|ok|fail
//	netUpnpStart(tcpPort, udpPort)                  abre as portas no roteador (assíncrono)
//	netUpnpStatus() -> state, externalIP, msg       state: idle|search|ok|fail ; msg: nogateway|denied|error:...
//	netUpnpStop()                                   fecha as portas abertas
//	netLanHostStart(name, tcpPort, rollback, ver) -> ok, err   responde às buscas da rede local
//	netLanHostStop()
//	netLanScanStart()                               procura salas na rede local (assíncrono, ~3,5 s)
//	netLanScanResults() -> state, {{name, ip, port, rollback, ver}, ...}   state: idle|scanning|done
//	netLanScanStop()
//	setClipboardString(text) -> bool                copia texto para a área de transferência
func netExtScriptInit(l *lua.LState) {
	luaRegister(l, "netLocalIP", func(l *lua.LState) int {
		l.Push(lua.LString(primaryLocalIPv4()))
		return 1
	})
	luaRegister(l, "netPublicIPStart", func(l *lua.LState) int {
		pubIP.Start(publicIPURLs)
		return 0
	})
	luaRegister(l, "netPublicIP", func(l *lua.LState) int {
		st, ip := pubIP.Status()
		l.Push(lua.LString(st))
		l.Push(lua.LString(ip))
		return 2
	})
	luaRegister(l, "netUpnpStart", func(l *lua.LState) int {
		tcp := l.OptInt(1, 7500)
		udp := l.OptInt(2, 0)
		upnp.Start(tcp, udp)
		return 0
	})
	luaRegister(l, "netUpnpStatus", func(l *lua.LState) int {
		st, ext, msg := upnp.Status()
		l.Push(lua.LString(st))
		l.Push(lua.LString(ext))
		l.Push(lua.LString(msg))
		return 3
	})
	luaRegister(l, "netUpnpStop", func(l *lua.LState) int {
		upnp.Close()
		return 0
	})
	luaRegister(l, "netLanHostStart", func(l *lua.LState) int {
		info := lanInfo{
			Name:     l.OptString(1, ""),
			Port:     l.OptInt(2, 7500),
			Rollback: lua.LVAsBool(l.Get(3)),
			Ver:      l.OptString(4, ""),
		}
		if err := lanHosting.Start(info, lanDiscoveryPort); err != nil {
			l.Push(lua.LFalse)
			l.Push(lua.LString(err.Error()))
			return 2
		}
		l.Push(lua.LTrue)
		return 1
	})
	luaRegister(l, "netLanHostStop", func(l *lua.LState) int {
		lanHosting.Stop()
		return 0
	})
	luaRegister(l, "netLanScanStart", func(l *lua.LState) int {
		targets, self := lanScanTargets()
		lanScan.Start(targets, self, lanDiscoveryPort, 3500*time.Millisecond)
		return 0
	})
	luaRegister(l, "netLanScanResults", func(l *lua.LState) int {
		st, rooms := lanScan.Results()
		t := l.NewTable()
		for _, r := range rooms {
			e := l.NewTable()
			e.RawSetString("name", lua.LString(r.Name))
			e.RawSetString("ip", lua.LString(r.IP))
			e.RawSetString("port", lua.LNumber(r.Port))
			e.RawSetString("rollback", lua.LBool(r.Rollback))
			e.RawSetString("ver", lua.LString(r.Ver))
			t.Append(e)
		}
		l.Push(lua.LString(st))
		l.Push(t)
		return 2
	})
	luaRegister(l, "netLanScanStop", func(l *lua.LState) int {
		lanScan.Stop()
		return 0
	})
	luaRegister(l, "setClipboardString", func(l *lua.LState) int {
		l.Push(lua.LBool(setClipboardText(l.OptString(1, ""))))
		return 1
	})
}
