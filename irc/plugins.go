package irc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"
)

var pluginHookNames = []string{
	"on_joinyield",
	"on_joinintercept",
	"on_join",
	"on_partyield",
	"on_partintercept",
	"on_part",
	"on_quityield",
	"on_quitintercept",
	"on_quit",
	"on_kickyield",
	"on_kickintercept",
	"on_kick",
	"on_nickyield",
	"on_nickintercept",
	"on_nick",
	"on_connectyield",
	"on_connectintercept",
	"on_connect",
	"on_validate_registeryield",
	"on_validate_registerintercept",
	"on_validate_register",
	"on_validate_joinyield",
	"on_validate_joinintercept",
	"on_validate_join",
	"on_privmsgyield",
	"on_privmsgintercept",
	"on_privmsg",
}

type Plugin struct {
	Name  string
	L     *lua.LState
	mu    sync.Mutex
	hooks map[string]*lua.LFunction
}

type PluginManager struct {
	server     *Server
	plugins    []*Plugin
	state      map[string]interface{}
	stateMutex sync.RWMutex
}

func NewPluginManager(server *Server) *PluginManager {
	pm := &PluginManager{
		server: server,
		state:  make(map[string]interface{}),
	}
	pm.LoadState()
	pm.LoadPlugins()
	return pm
}

func (pm *PluginManager) LoadState() {
	data, err := os.ReadFile(filepath.Join("plugins", "state.json"))
	if err != nil {
		return
	}

	var rawState map[string]interface{}
	if err := json.Unmarshal(data, &rawState); err != nil {
		return
	}

	pm.stateMutex.Lock()
	defer pm.stateMutex.Unlock()
	pm.state = rawState
}

func (pm *PluginManager) saveStateFile() error {
	pm.stateMutex.RLock()
	rawState := make(map[string]interface{}, len(pm.state))
	for key, value := range pm.state {
		rawState[key] = value
	}
	pm.stateMutex.RUnlock()

	data, err := json.MarshalIndent(rawState, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join("plugins", "state.json"), data, 0644)
}

// goToLua converts a Go value to a Lua value, recursively for maps/slices.
func (pm *PluginManager) goToLua(L *lua.LState, v interface{}) lua.LValue {
	switch val := v.(type) {
	case nil:
		return lua.LNil
	case bool:
		return lua.LBool(val)
	case float64:
		return lua.LNumber(val)
	case int:
		return lua.LNumber(val)
	case int64:
		return lua.LNumber(val)
	case string:
		return lua.LString(val)
	case map[string]interface{}:
		tbl := L.NewTable()
		for k, v2 := range val {
			L.SetField(tbl, k, pm.goToLua(L, v2))
		}
		return tbl
	case []interface{}:
		tbl := L.NewTable()
		for i, v2 := range val {
			tbl.RawSetInt(i+1, pm.goToLua(L, v2))
		}
		return tbl
	default:
		return lua.LString(fmt.Sprintf("%v", val))
	}
}

// luaToGo converts a Lua value to a Go value for JSON serialisation and state storage.
func luaToGo(v lua.LValue) interface{} {
	switch val := v.(type) {
	case lua.LBool:
		return bool(val)
	case lua.LNumber:
		return float64(val)
	case lua.LString:
		return string(val)
	case *lua.LTable:
		result := make(map[string]interface{})
		val.ForEach(func(key, value lua.LValue) {
			result[lua.LVAsString(key)] = luaToGo(value)
		})
		return result
	default:
		return nil
	}
}

// registerErgoModule preloads the "ergo" module into a Lua state.
func (pm *PluginManager) registerErgoModule(L *lua.LState, plugin *Plugin) {
	L.PreloadModule("ergo", func(L *lua.LState) int {
		mod := L.NewTable()
		L.SetField(mod, "SERVER_NAME", lua.LString(pm.server.name))

		set := func(name string, fn func(*lua.LState) int) {
			L.SetField(mod, name, L.NewFunction(fn))
		}

		set("send_message", func(L *lua.LState) int { return pm.luaSendInternal(L, "NOTICE") })
		set("send_notice", func(L *lua.LState) int { return pm.luaSendInternal(L, "NOTICE") })
		set("send_privmsg", func(L *lua.LState) int { return pm.luaSendInternal(L, "PRIVMSG") })
		set("get_client", pm.luaGetClient)
		set("has_role_capab", pm.luaHasRoleCapab)
		set("kick", pm.luaKick)
		set("kill", pm.luaKill)
		set("kline", pm.luaKline)
		set("save_state", pm.luaSaveState)
		set("load_state", pm.luaLoadState)
		set("json_parse", pm.luaJSONParse)
		set("json_encode", pm.luaJSONEncode)
		set("time_now", pm.luaTimeNow)
		set("time_format", pm.luaTimeFormat)
		set("reload_plugins", pm.luaReloadPlugins)
		set("get_channel_members", pm.luaGetChannelMembers)
		set("spawn", func(L *lua.LState) int { return pm.luaSpawn(L, plugin) })
		set("http_get", func(L *lua.LState) int { return pm.luaHTTPGet(L, plugin) })
		set("http_post", func(L *lua.LState) int { return pm.luaHTTPPost(L, plugin) })

		L.Push(mod)
		return 1
	})
}

func (pm *PluginManager) loadPlugin(path string, fileName string) (*Plugin, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	plugin := &Plugin{
		Name:  fileName,
		hooks: make(map[string]*lua.LFunction),
	}
	plugin.L = lua.NewState()
	pm.registerErgoModule(plugin.L, plugin)

	if err := plugin.L.DoString(string(source)); err != nil {
		plugin.L.Close()
		return nil, err
	}

	for _, hookName := range pluginHookNames {
		if lf, ok := plugin.L.GetGlobal(hookName).(*lua.LFunction); ok {
			plugin.hooks[hookName] = lf
		}
	}

	return plugin, nil
}

func (pm *PluginManager) invokePluginHook(plugin *Plugin, hookName string, args ...lua.LValue) (lua.LValue, bool, error) {
	fn, ok := plugin.hooks[hookName]
	if !ok {
		return lua.LNil, false, nil
	}

	plugin.mu.Lock()
	defer plugin.mu.Unlock()

	if err := plugin.L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, args...); err != nil {
		return lua.LNil, true, err
	}
	result := plugin.L.Get(-1)
	plugin.L.Pop(1)
	return result, true, nil
}

func (pm *PluginManager) invokePluginCallback(plugin *Plugin, callback *lua.LFunction, args ...lua.LValue) (lua.LValue, error) {
	plugin.mu.Lock()
	defer plugin.mu.Unlock()

	if err := plugin.L.CallByParam(lua.P{Fn: callback, NRet: 1, Protect: true}, args...); err != nil {
		return lua.LNil, err
	}
	result := plugin.L.Get(-1)
	plugin.L.Pop(1)
	return result, nil
}

func (pm *PluginManager) invokeYieldHook(plugin *Plugin, hookName string, args ...lua.LValue) {
	if _, hasYield := plugin.hooks[hookName]; !hasYield {
		return
	}

	argv := append([]lua.LValue(nil), args...)
	go func(p *Plugin, hook string, params []lua.LValue) {
		_, called, err := pm.invokePluginHook(p, hook, params...)
		if called && err != nil {
			pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s %s", p.Name, hook), err.Error())
		}
	}(plugin, hookName, argv)
}

func (pm *PluginManager) invokeInterceptOrLegacyHook(plugin *Plugin, interceptHook, legacyHook string, args ...lua.LValue) (lua.LValue, bool, string, error) {
	hookName := interceptHook
	if _, hasIntercept := plugin.hooks[hookName]; !hasIntercept {
		hookName = legacyHook
	}

	result, called, err := pm.invokePluginHook(plugin, hookName, args...)
	return result, called, hookName, err
}

func (pm *PluginManager) luaSendInternal(L *lua.LState, command string) int {
	target := L.CheckString(1)
	message := L.CheckString(2)

	if client := pm.server.clients.Get(target); client != nil {
		for _, session := range client.Sessions() {
			session.Send(nil, pm.server.name, command, client.Nick(), message)
		}
	} else if channel := pm.server.channels.Get(target); channel != nil {
		for _, member := range channel.Members() {
			for _, session := range member.Sessions() {
				session.Send(nil, pm.server.name, command, channel.Name(), message)
			}
		}
	}
	return 0
}

func (pm *PluginManager) luaGetClient(L *lua.LState) int {
	nick := L.CheckString(1)
	client := pm.server.clients.Get(nick)
	if client == nil {
		L.Push(lua.LNil)
		return 1
	}
	details := client.Details()
	tbl := L.NewTable()
	L.SetField(tbl, "nick", lua.LString(details.nick))
	L.SetField(tbl, "username", lua.LString(details.username))
	L.SetField(tbl, "hostname", lua.LString(details.hostname))
	L.SetField(tbl, "realname", lua.LString(details.realname))
	L.Push(tbl)
	return 1
}

func (pm *PluginManager) luaHasRoleCapab(L *lua.LState) int {
	nick := L.CheckString(1)
	capab := L.CheckString(2)
	client := pm.server.clients.Get(nick)
	if client == nil || client.Oper() == nil {
		L.Push(lua.LFalse)
		return 1
	}
	L.Push(lua.LBool(client.Oper().HasRoleCapab(capab)))
	return 1
}

func (pm *PluginManager) luaKick(L *lua.LState) int {
	channel := L.CheckString(1)
	nick := L.CheckString(2)
	reason := L.OptString(3, "")
	ch := pm.server.channels.Get(channel)
	target := pm.server.clients.Get(nick)
	if ch != nil && target != nil {
		ch.Kick(nil, target, reason, nil, true)
	}
	return 0
}

func (pm *PluginManager) luaKill(L *lua.LState) int {
	nick := L.CheckString(1)
	reason := L.OptString(2, "")
	target := pm.server.clients.Get(nick)
	if target != nil {
		target.Quit("Killed: "+reason, nil)
		target.destroy(nil)
	}
	return 0
}

func (pm *PluginManager) luaKline(L *lua.LState) int {
	mask := L.CheckString(1)
	durationStr := L.CheckString(2)
	reason := L.OptString(3, "")
	duration, err := time.ParseDuration(durationStr)
	if err != nil {
		L.RaiseError("invalid duration: %s", durationStr)
		return 0
	}
	pm.server.klines.AddMask(mask, duration, false, reason, reason, pm.server.name)
	return 0
}

func (pm *PluginManager) luaSaveState(L *lua.LState) int {
	key := L.CheckString(1)
	value := L.Get(2)
	pm.stateMutex.Lock()
	pm.state[key] = luaToGo(value)
	pm.stateMutex.Unlock()
	if err := pm.saveStateFile(); err != nil {
		L.RaiseError("save_state failed: %s", err.Error())
	}
	return 0
}

func (pm *PluginManager) luaLoadState(L *lua.LState) int {
	key := L.CheckString(1)
	pm.stateMutex.RLock()
	value, ok := pm.state[key]
	pm.stateMutex.RUnlock()
	if !ok {
		L.Push(lua.LNil)
		return 1
	}
	L.Push(pm.goToLua(L, value))
	return 1
}

func (pm *PluginManager) luaJSONParse(L *lua.LState) int {
	input := L.CheckString(1)
	var value interface{}
	if err := json.Unmarshal([]byte(input), &value); err != nil {
		L.RaiseError("json_parse failed: %s", err.Error())
		return 0
	}
	L.Push(pm.goToLua(L, value))
	return 1
}

func (pm *PluginManager) luaJSONEncode(L *lua.LState) int {
	data, err := json.Marshal(luaToGo(L.Get(1)))
	if err != nil {
		L.RaiseError("json_encode failed: %s", err.Error())
		return 0
	}
	L.Push(lua.LString(string(data)))
	return 1
}

func (pm *PluginManager) luaTimeNow(L *lua.LState) int {
	L.Push(lua.LNumber(time.Now().Unix()))
	return 1
}

func (pm *PluginManager) luaTimeFormat(L *lua.LState) int {
	layout := L.CheckString(1)
	timestamp := int64(L.CheckNumber(2))
	L.Push(lua.LString(time.Unix(timestamp, 0).Format(layout)))
	return 1
}

func (pm *PluginManager) luaReloadPlugins(L *lua.LState) int {
	pm.ReloadPlugins()
	return 0
}

func (pm *PluginManager) luaGetChannelMembers(L *lua.LState) int {
	channelName := L.CheckString(1)
	channel := pm.server.channels.Get(channelName)
	tbl := L.NewTable()
	if channel != nil {
		for i, member := range channel.Members() {
			tbl.RawSetInt(i+1, lua.LString(member.Nick()))
		}
	}
	L.Push(tbl)
	return 1
}

// luaSpawn runs a callback asynchronously without blocking the current hook.
func (pm *PluginManager) luaSpawn(L *lua.LState, plugin *Plugin) int {
	callback := L.CheckFunction(1)
	args := make([]lua.LValue, 0, L.GetTop()-1)
	for i := 2; i <= L.GetTop(); i++ {
		args = append(args, L.Get(i))
	}

	go func() {
		_, err := pm.invokePluginCallback(plugin, callback, args...)
		if err != nil {
			pm.server.logger.Error("plugins", "Error in spawned callback", err.Error())
		}
	}()

	return 0
}

func (pm *PluginManager) luaHTTPGet(L *lua.LState, plugin *Plugin) int {
	url := L.CheckString(1)
	callback := L.CheckFunction(2)
	var userdata lua.LValue = lua.LNil
	if L.GetTop() >= 3 {
		userdata = L.Get(3)
	}
	go func() {
		resp, err := http.Get(url) //nolint:noctx
		pm.handleHTTPResponse(plugin, callback, userdata, resp, err)
	}()
	return 0
}

func (pm *PluginManager) luaHTTPPost(L *lua.LState, plugin *Plugin) int {
	url := L.CheckString(1)
	body := L.CheckString(2)
	callback := L.CheckFunction(3)
	var userdata lua.LValue = lua.LNil
	if L.GetTop() >= 4 {
		userdata = L.Get(4)
	}
	go func() {
		resp, err := http.Post(url, "application/json", bytes.NewBufferString(body)) //nolint:noctx
		pm.handleHTTPResponse(plugin, callback, userdata, resp, err)
	}()
	return 0
}

func (pm *PluginManager) handleHTTPResponse(plugin *Plugin, callback *lua.LFunction, userdata lua.LValue, resp *http.Response, err error) {
	var body string
	var status int64
	var errStr string

	if err != nil {
		errStr = err.Error()
	} else {
		status = int64(resp.StatusCode)
		defer resp.Body.Close()
		payload, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			errStr = readErr.Error()
		} else {
			body = string(payload)
		}
	}

	_, hookErr := pm.invokePluginCallback(plugin, callback,
		lua.LString(body),
		lua.LNumber(status),
		lua.LString(errStr),
		userdata,
	)
	if hookErr != nil {
		pm.server.logger.Error("plugins", "Error in HTTP callback", hookErr.Error())
	}
}

func (pm *PluginManager) ReloadPlugins() {
	for _, plugin := range pm.plugins {
		plugin.L.Close()
	}
	pm.plugins = nil
	pm.LoadPlugins()
}

func (pm *PluginManager) LoadPlugins() {
	pluginsDir := "plugins"
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		pm.server.logger.Error("plugins", "Failed to create plugins directory", err.Error())
		return
	}

	files, err := os.ReadDir(pluginsDir)
	if err != nil {
		pm.server.logger.Error("plugins", "Failed to read plugins directory", err.Error())
		return
	}

	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".lua" {
			continue
		}

		path := filepath.Join(pluginsDir, file.Name())
		plugin, err := pm.loadPlugin(path, file.Name())
		if err != nil {
			pm.server.logger.Error("plugins", fmt.Sprintf("Failed to load plugin %s", file.Name()), err.Error())
			continue
		}

		pm.plugins = append(pm.plugins, plugin)
		pm.server.logger.Info("plugins", fmt.Sprintf("Loaded plugin %s", file.Name()))
	}
}

func (pm *PluginManager) OnJoin(clientNick string, channelName string) {
	for _, plugin := range pm.plugins {
		pm.invokeYieldHook(plugin, "on_joinyield", lua.LString(clientNick), lua.LString(channelName))
		_, called, hookName, err := pm.invokeInterceptOrLegacyHook(plugin, "on_joinintercept", "on_join", lua.LString(clientNick), lua.LString(channelName))
		if called && err != nil {
			pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s %s", plugin.Name, hookName), err.Error())
		}
	}
}

func (pm *PluginManager) OnPart(clientNick string, channelName string, reason string) {
	for _, plugin := range pm.plugins {
		pm.invokeYieldHook(plugin, "on_partyield", lua.LString(clientNick), lua.LString(channelName), lua.LString(reason))
		_, called, hookName, err := pm.invokeInterceptOrLegacyHook(plugin, "on_partintercept", "on_part", lua.LString(clientNick), lua.LString(channelName), lua.LString(reason))
		if called && err != nil {
			pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s %s", plugin.Name, hookName), err.Error())
		}
	}
}

func (pm *PluginManager) OnQuit(clientNick string, reason string) {
	for _, plugin := range pm.plugins {
		pm.invokeYieldHook(plugin, "on_quityield", lua.LString(clientNick), lua.LString(reason))
		_, called, hookName, err := pm.invokeInterceptOrLegacyHook(plugin, "on_quitintercept", "on_quit", lua.LString(clientNick), lua.LString(reason))
		if called && err != nil {
			pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s %s", plugin.Name, hookName), err.Error())
		}
	}
}

func (pm *PluginManager) OnKick(clientNick string, channelName string, targetNick string, reason string) {
	for _, plugin := range pm.plugins {
		pm.invokeYieldHook(plugin, "on_kickyield", lua.LString(clientNick), lua.LString(channelName), lua.LString(targetNick), lua.LString(reason))
		_, called, hookName, err := pm.invokeInterceptOrLegacyHook(plugin, "on_kickintercept", "on_kick", lua.LString(clientNick), lua.LString(channelName), lua.LString(targetNick), lua.LString(reason))
		if called && err != nil {
			pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s %s", plugin.Name, hookName), err.Error())
		}
	}
}

func (pm *PluginManager) OnNick(oldNick string, newNick string) {
	for _, plugin := range pm.plugins {
		pm.invokeYieldHook(plugin, "on_nickyield", lua.LString(oldNick), lua.LString(newNick))
		_, called, hookName, err := pm.invokeInterceptOrLegacyHook(plugin, "on_nickintercept", "on_nick", lua.LString(oldNick), lua.LString(newNick))
		if called && err != nil {
			pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s %s", plugin.Name, hookName), err.Error())
		}
	}
}

func (pm *PluginManager) OnConnect(clientNick string, ipAddr string) {
	for _, plugin := range pm.plugins {
		pm.invokeYieldHook(plugin, "on_connectyield", lua.LString(clientNick), lua.LString(ipAddr))
		_, called, hookName, err := pm.invokeInterceptOrLegacyHook(plugin, "on_connectintercept", "on_connect", lua.LString(clientNick), lua.LString(ipAddr))
		if called && err != nil {
			pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s %s", plugin.Name, hookName), err.Error())
		}
	}
}

func (pm *PluginManager) OnValidateRegister(nick, username, hostname, ip string) *string {
	for _, plugin := range pm.plugins {
		pm.invokeYieldHook(plugin, "on_validate_registeryield",
			lua.LString(nick), lua.LString(username), lua.LString(hostname), lua.LString(ip))

		result, called, hookName, err := pm.invokeInterceptOrLegacyHook(plugin, "on_validate_registerintercept", "on_validate_register",
			lua.LString(nick), lua.LString(username), lua.LString(hostname), lua.LString(ip))
		if !called {
			continue
		}
		if err != nil {
			pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s %s", plugin.Name, hookName), err.Error())
			continue
		}
		if s, ok := result.(lua.LString); ok {
			str := string(s)
			return &str
		}
	}
	return nil
}

func (pm *PluginManager) OnValidateJoin(nick, channel string) (bool, string, string, bool, bool) {
	for _, plugin := range pm.plugins {
		pm.invokeYieldHook(plugin, "on_validate_joinyield", lua.LString(nick), lua.LString(channel))

		result, called, hookName, err := pm.invokeInterceptOrLegacyHook(plugin, "on_validate_joinintercept", "on_validate_join",
			lua.LString(nick), lua.LString(channel))
		if !called {
			continue
		}
		if err != nil {
			pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s %s", plugin.Name, hookName), err.Error())
			continue
		}

		// Plain string → block with that reason
		if s, ok := result.(lua.LString); ok {
			return true, string(s), "", false, false
		}

		// Table → structured response
		tbl, ok := result.(*lua.LTable)
		if !ok {
			continue
		}

		var blocked, silent, stealth bool
		var reason, redirect string

		if blockVal, ok := tbl.RawGetString("block").(lua.LString); ok {
			blocked = true
			reason = string(blockVal)
		}
		if redirectVal, ok := tbl.RawGetString("redirect").(lua.LString); ok {
			redirect = string(redirectVal)
		}
		if silentVal, ok := tbl.RawGetString("silent").(lua.LBool); ok {
			silent = bool(silentVal)
		}
		if stealthVal, ok := tbl.RawGetString("stealth").(lua.LBool); ok {
			stealth = bool(stealthVal)
		}

		return blocked, reason, redirect, silent, stealth
	}

	return false, "", "", false, false
}

func (pm *PluginManager) OnPrivmsg(clientNick string, target string, message string) *string {
	currentMessage := message

	for _, plugin := range pm.plugins {
		pm.invokeYieldHook(plugin, "on_privmsgyield", lua.LString(clientNick), lua.LString(target), lua.LString(currentMessage))

		result, called, hookName, err := pm.invokeInterceptOrLegacyHook(plugin, "on_privmsgintercept", "on_privmsg",
			lua.LString(clientNick), lua.LString(target), lua.LString(currentMessage))
		if !called {
			continue
		}
		if err != nil {
			pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s %s", plugin.Name, hookName), err.Error())
			continue
		}

		if result == lua.LNil {
			return nil
		}
		if s, ok := result.(lua.LString); ok {
			currentMessage = string(s)
		}
	}

	return &currentMessage
}
