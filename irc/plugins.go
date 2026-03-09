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

	"go.starlark.net/starlark"
)

type Plugin struct {
	Name    string
	Globals starlark.StringDict
}

type PluginManager struct {
	server      *Server
	plugins     []Plugin
	state       map[string]starlark.Value
	stateMutex  sync.RWMutex
	predeclared starlark.StringDict
}

func NewPluginManager(server *Server) *PluginManager {
	pm := &PluginManager{
		server: server,
		state:  make(map[string]starlark.Value),
	}
	pm.initPredeclared()
	pm.LoadState()
	pm.LoadPlugins()
	return pm
}

func (pm *PluginManager) initPredeclared() {
	pm.predeclared = starlark.StringDict{
		"SERVER_NAME":    starlark.String(pm.server.name),
		"send_message":   starlark.NewBuiltin("send_message", pm.starlarkSendMessage),
		"send_notice":    starlark.NewBuiltin("send_notice", pm.starlarkSendNotice),
		"send_privmsg":   starlark.NewBuiltin("send_privmsg", pm.starlarkSendPrivmsg),
		"get_client":     starlark.NewBuiltin("get_client", pm.starlarkGetClient),
		"has_role_capab": starlark.NewBuiltin("has_role_capab", pm.starlarkHasRoleCapab),
		"kick":           starlark.NewBuiltin("kick", pm.starlarkKick),
		"kill":           starlark.NewBuiltin("kill", pm.starlarkKill),
		"kline":          starlark.NewBuiltin("kline", pm.starlarkKline),
		"save_state":     starlark.NewBuiltin("save_state", pm.starlarkSaveState),
		"load_state":     starlark.NewBuiltin("load_state", pm.starlarkLoadState),
		"http_get":       starlark.NewBuiltin("http_get", pm.starlarkHttpGet),
		"http_post":      starlark.NewBuiltin("http_post", pm.starlarkHttpPost),
		"json_parse":     starlark.NewBuiltin("json_parse", pm.starlarkJsonParse),
		"json_encode":    starlark.NewBuiltin("json_encode", pm.starlarkJsonEncode),
		"time_now":       starlark.NewBuiltin("time_now", pm.starlarkTimeNow),
		"time_format":    starlark.NewBuiltin("time_format", pm.starlarkTimeFormat),
		"reload_plugins": starlark.NewBuiltin("reload_plugins", pm.starlarkReloadPlugins),
	}
}

func (pm *PluginManager) newThread(name string) *starlark.Thread {
	return &starlark.Thread{
		Name: name,
		Print: func(_ *starlark.Thread, msg string) {
			pm.server.logger.Info("plugins", msg)
		},
	}
}

func (pm *PluginManager) LoadState() {
	statePath := filepath.Join("plugins", "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		return
	}
	var rawState map[string]interface{}
	if err := json.Unmarshal(data, &rawState); err != nil {
		return
	}
	pm.stateMutex.Lock()
	defer pm.stateMutex.Unlock()
	for k, v := range rawState {
		pm.state[k] = pm.toStarlarkValue(v)
	}
}

func (pm *PluginManager) toStarlarkValue(v interface{}) starlark.Value {
	switch val := v.(type) {
	case string:
		return starlark.String(val)
	case float64:
		return starlark.Float(val)
	case bool:
		return starlark.Bool(val)
	case []interface{}:
		list := starlark.NewList(nil)
		for _, item := range val {
			list.Append(pm.toStarlarkValue(item))
		}
		return list
	case map[string]interface{}:
		dict := starlark.NewDict(len(val))
		for k, item := range val {
			dict.SetKey(starlark.String(k), pm.toStarlarkValue(item))
		}
		return dict
	default:
		return starlark.None
	}
}

func (pm *PluginManager) fromStarlarkValue(v starlark.Value) interface{} {
	switch val := v.(type) {
	case starlark.String:
		return val.GoString()
	case starlark.Float:
		return float64(val)
	case starlark.Int:
		i, _ := val.Int64()
		return i
	case starlark.Bool:
		return bool(val)
	case *starlark.List:
		res := make([]interface{}, val.Len())
		for i := 0; i < val.Len(); i++ {
			res[i] = pm.fromStarlarkValue(val.Index(i))
		}
		return res
	case *starlark.Dict:
		res := make(map[string]interface{})
		for _, item := range val.Items() {
			if k, ok := item[0].(starlark.String); ok {
				res[k.GoString()] = pm.fromStarlarkValue(item[1])
			}
		}
		return res
	default:
		return nil
	}
}

func (pm *PluginManager) starlarkSaveState(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var key string
	var value starlark.Value
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "key", &key, "value", &value); err != nil {
		return starlark.None, err
	}
	pm.stateMutex.Lock()
	pm.state[key] = value

	// Save to file (holding lock for consistency, though maybe slow)
	rawState := make(map[string]interface{})
	for k, v := range pm.state {
		rawState[k] = pm.fromStarlarkValue(v)
	}
	pm.stateMutex.Unlock()
	data, err := json.MarshalIndent(rawState, "", "  ")
	if err != nil {
		return starlark.None, err
	}
	os.WriteFile(filepath.Join("plugins", "state.json"), data, 0644)
	return starlark.None, nil
}

func (pm *PluginManager) starlarkLoadState(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var key string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "key", &key); err != nil {
		return starlark.None, err
	}
	pm.stateMutex.RLock()
	defer pm.stateMutex.RUnlock()
	if val, ok := pm.state[key]; ok {
		return val, nil
	}
	return starlark.None, nil
}

func (pm *PluginManager) starlarkHasRoleCapab(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var nick, capab string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "nick", &nick, "capab", &capab); err != nil {
		return starlark.None, err
	}
	client := pm.server.clients.Get(nick)
	if client == nil {
		return starlark.Bool(false), nil
	}
	oper := client.Oper()
	if oper == nil {
		return starlark.Bool(false), nil
	}
	return starlark.Bool(oper.HasRoleCapab(capab)), nil
}

func (pm *PluginManager) starlarkKick(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var channel, nick, reason string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "channel", &channel, "nick", &nick, "reason?", &reason); err != nil {
		return starlark.None, err
	}
	ch := pm.server.channels.Get(channel)
	target := pm.server.clients.Get(nick)
	if ch != nil && target != nil {
		ch.Kick(nil, target, reason, nil, true)
	}
	return starlark.None, nil
}

func (pm *PluginManager) starlarkKill(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var nick, reason string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "nick", &nick, "reason?", &reason); err != nil {
		return starlark.None, err
	}
	target := pm.server.clients.Get(nick)
	if target != nil {
		target.Quit("Killed: "+reason, nil)
		target.destroy(nil)
	}
	return starlark.None, nil
}

func (pm *PluginManager) starlarkKline(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var mask, durationStr, reason string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "mask", &mask, "duration", &durationStr, "reason?", &reason); err != nil {
		return starlark.None, err
	}
	dur, err := time.ParseDuration(durationStr)
	if err != nil {
		// Try to parse simple durations like 1h, 1d
		dur, err = time.ParseDuration(durationStr)
		if err != nil {
			return starlark.None, fmt.Errorf("invalid duration: %s", durationStr)
		}
	}
	pm.server.klines.AddMask(mask, dur, false, reason, reason, pm.server.name)
	return starlark.None, nil
}

func (pm *PluginManager) starlarkTimeNow(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return starlark.MakeInt64(time.Now().Unix()), nil
}

func (pm *PluginManager) starlarkTimeFormat(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var layout string
	var timestamp int64
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "layout", &layout, "timestamp", &timestamp); err != nil {
		return starlark.None, err
	}
	t := time.Unix(timestamp, 0)
	return starlark.String(t.Format(layout)), nil
}

func (pm *PluginManager) starlarkReloadPlugins(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	pm.ReloadPlugins()
	return starlark.None, nil
}

func (pm *PluginManager) ReloadPlugins() {
	pm.stateMutex.Lock()
	defer pm.stateMutex.Unlock()
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
		if !file.IsDir() && filepath.Ext(file.Name()) == ".star" {
			path := filepath.Join(pluginsDir, file.Name())
			thread := pm.newThread("load-" + file.Name())
			globals, err := starlark.ExecFile(thread, path, nil, pm.predeclared)
			if err != nil {
				pm.server.logger.Error("plugins", fmt.Sprintf("Failed to load plugin %s", file.Name()), err.Error())
				continue
			}
			pm.plugins = append(pm.plugins, Plugin{
				Name:    file.Name(),
				Globals: globals,
			})
			pm.server.logger.Info("plugins", fmt.Sprintf("Loaded plugin %s", file.Name()))
		}
	}
}

func (pm *PluginManager) starlarkJsonParse(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var s string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "s", &s); err != nil {
		return starlark.None, err
	}
	var v interface{}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return starlark.None, err
	}
	return pm.toStarlarkValue(v), nil
}

func (pm *PluginManager) starlarkJsonEncode(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var v starlark.Value
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "v", &v); err != nil {
		return starlark.None, err
	}
	data, err := json.Marshal(pm.fromStarlarkValue(v))
	if err != nil {
		return starlark.None, err
	}
	return starlark.String(string(data)), nil
}

func (pm *PluginManager) starlarkHttpGet(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var url string
	var callback starlark.Callable
	var userdata starlark.Value = starlark.None
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "url", &url, "callback", &callback, "userdata?", &userdata); err != nil {
		return starlark.None, err
	}

	go func() {
		resp, err := http.Get(url)
		pm.handleHttpResponse(callback, userdata, resp, err)
	}()

	return starlark.None, nil
}

func (pm *PluginManager) starlarkHttpPost(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var url, body string
	var callback starlark.Callable
	var userdata starlark.Value = starlark.None
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "url", &url, "body", &body, "callback", &callback, "userdata?", &userdata); err != nil {
		return starlark.None, err
	}

	go func() {
		resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
		pm.handleHttpResponse(callback, userdata, resp, err)
	}()

	return starlark.None, nil
}

func (pm *PluginManager) handleHttpResponse(callback starlark.Callable, userdata starlark.Value, resp *http.Response, err error) {
	var body string
	var status int
	var errStr string

	if err != nil {
		errStr = err.Error()
	} else {
		status = resp.StatusCode
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		body = string(b)
	}

	thread := pm.newThread("http-callback")
	_, hookErr := starlark.Call(thread, callback, starlark.Tuple{
		starlark.String(body),
		starlark.MakeInt(status),
		starlark.String(errStr),
		userdata,
	}, nil)

	if hookErr != nil {
		pm.server.logger.Error("plugins", "Error in HTTP callback", hookErr.Error())
	}
}

func (pm *PluginManager) starlarkSendMessage(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return pm.starlarkSendInternal("NOTICE", args, kwargs)
}

func (pm *PluginManager) starlarkSendNotice(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return pm.starlarkSendInternal("NOTICE", args, kwargs)
}

func (pm *PluginManager) starlarkSendPrivmsg(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return pm.starlarkSendInternal("PRIVMSG", args, kwargs)
}

func (pm *PluginManager) starlarkSendInternal(command string, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var target, message string
	if err := starlark.UnpackArgs("send", args, kwargs, "target", &target, "message", &message); err != nil {
		return starlark.None, err
	}

	// Find target client or channel
	if t := pm.server.clients.Get(target); t != nil {
		for _, session := range t.Sessions() {
			session.Send(nil, pm.server.name, command, t.Nick(), message)
		}
	} else if ch := pm.server.channels.Get(target); ch != nil {
		for _, member := range ch.Members() {
			for _, session := range member.Sessions() {
				session.Send(nil, pm.server.name, command, ch.Name(), message)
			}
		}
	}

	return starlark.None, nil
}

func (pm *PluginManager) starlarkGetClient(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var nick string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "nick", &nick); err != nil {
		return starlark.None, err
	}

	client := pm.server.clients.Get(nick)
	if client == nil {
		return starlark.None, nil
	}

	details := client.Details()

	dict := starlark.NewDict(2)
	dict.SetKey(starlark.String("nick"), starlark.String(details.nick))
	dict.SetKey(starlark.String("username"), starlark.String(details.username))
	dict.SetKey(starlark.String("hostname"), starlark.String(details.hostname))
	dict.SetKey(starlark.String("realname"), starlark.String(details.realname))

	return dict, nil
}

func (pm *PluginManager) OnJoin(clientNick string, channelName string) {
	for _, plugin := range pm.plugins {
		if fn, ok := plugin.Globals["on_join"]; ok {
			if starlarkFn, ok := fn.(starlark.Callable); ok {
				thread := pm.newThread("on_join")
				_, err := starlark.Call(thread, starlarkFn, starlark.Tuple{starlark.String(clientNick), starlark.String(channelName)}, nil)
				if err != nil {
					pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s on_join", plugin.Name), err.Error())
				}
			}
		}
	}
}

func (pm *PluginManager) OnPart(clientNick string, channelName string, reason string) {
	for _, plugin := range pm.plugins {
		if fn, ok := plugin.Globals["on_part"]; ok {
			if starlarkFn, ok := fn.(starlark.Callable); ok {
				thread := pm.newThread("on_part")
				_, err := starlark.Call(thread, starlarkFn, starlark.Tuple{starlark.String(clientNick), starlark.String(channelName), starlark.String(reason)}, nil)
				if err != nil {
					pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s on_part", plugin.Name), err.Error())
				}
			}
		}
	}
}

func (pm *PluginManager) OnQuit(clientNick string, reason string) {
	for _, plugin := range pm.plugins {
		if fn, ok := plugin.Globals["on_quit"]; ok {
			if starlarkFn, ok := fn.(starlark.Callable); ok {
				thread := pm.newThread("on_quit")
				_, err := starlark.Call(thread, starlarkFn, starlark.Tuple{starlark.String(clientNick), starlark.String(reason)}, nil)
				if err != nil {
					pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s on_quit", plugin.Name), err.Error())
				}
			}
		}
	}
}

func (pm *PluginManager) OnKick(clientNick string, channelName string, targetNick string, reason string) {
	for _, plugin := range pm.plugins {
		if fn, ok := plugin.Globals["on_kick"]; ok {
			if starlarkFn, ok := fn.(starlark.Callable); ok {
				thread := pm.newThread("on_kick")
				_, err := starlark.Call(thread, starlarkFn, starlark.Tuple{starlark.String(clientNick), starlark.String(channelName), starlark.String(targetNick), starlark.String(reason)}, nil)
				if err != nil {
					pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s on_kick", plugin.Name), err.Error())
				}
			}
		}
	}
}

func (pm *PluginManager) OnNick(oldNick string, newNick string) {
	for _, plugin := range pm.plugins {
		if fn, ok := plugin.Globals["on_nick"]; ok {
			if starlarkFn, ok := fn.(starlark.Callable); ok {
				thread := pm.newThread("on_nick")
				_, err := starlark.Call(thread, starlarkFn, starlark.Tuple{starlark.String(oldNick), starlark.String(newNick)}, nil)
				if err != nil {
					pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s on_nick", plugin.Name), err.Error())
				}
			}
		}
	}
}

func (pm *PluginManager) OnConnect(clientNick string, ipAddr string) {
	for _, plugin := range pm.plugins {
		if fn, ok := plugin.Globals["on_connect"]; ok {
			if starlarkFn, ok := fn.(starlark.Callable); ok {
				thread := pm.newThread("on_connect")
				_, err := starlark.Call(thread, starlarkFn, starlark.Tuple{starlark.String(clientNick), starlark.String(ipAddr)}, nil)
				if err != nil {
					pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s on_connect", plugin.Name), err.Error())
				}
			}
		}
	}
}

// OnValidateRegister triggers during registration. If it returns a string, registration is blocked.
func (pm *PluginManager) OnValidateRegister(nick, username, hostname, ip string) *string {
	for _, plugin := range pm.plugins {
		if fn, ok := plugin.Globals["on_validate_register"]; ok {
			if starlarkFn, ok := fn.(starlark.Callable); ok {
				thread := pm.newThread("on_validate_register")
				res, err := starlark.Call(thread, starlarkFn, starlark.Tuple{
					starlark.String(nick),
					starlark.String(username),
					starlark.String(hostname),
					starlark.String(ip),
				}, nil)
				if err != nil {
					pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s on_validate_register", plugin.Name), err.Error())
					continue
				}

				if str, ok := res.(starlark.String); ok {
					reason := str.GoString()
					return &reason
				}
			}
		}
	}
	return nil
}

// OnValidateJoin triggers during JOIN. It can return None to allow, a string to block, or a dict to redirect/silence/hide.
// Returns (blocked bool, reason string, newChannel string, silent bool, stealth bool)
func (pm *PluginManager) OnValidateJoin(nick, channel string) (bool, string, string, bool, bool) {
	for _, plugin := range pm.plugins {
		if fn, ok := plugin.Globals["on_validate_join"]; ok {
			if starlarkFn, ok := fn.(starlark.Callable); ok {
				thread := pm.newThread("on_validate_join")
				res, err := starlark.Call(thread, starlarkFn, starlark.Tuple{
					starlark.String(nick),
					starlark.String(channel),
				}, nil)
				if err != nil {
					pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s on_validate_join", plugin.Name), err.Error())
					continue
				}

				if str, ok := res.(starlark.String); ok {
					return true, str.GoString(), "", false, false
				}

				if dict, ok := res.(*starlark.Dict); ok {
					var blocked, silent, stealth bool
					var reason, redirect string
					for _, item := range dict.Items() {
						keyStr, okK := item[0].(starlark.String)
						if !okK {
							continue
						}
						key := keyStr.GoString()
						if key == "block" {
							if valStr, okV := item[1].(starlark.String); okV {
								blocked = true
								reason = valStr.GoString()
							}
						} else if key == "redirect" {
							if valStr, okV := item[1].(starlark.String); okV {
								redirect = valStr.GoString()
							}
						} else if key == "silent" {
							if valBool, okB := item[1].(starlark.Bool); okB {
								silent = bool(valBool)
							}
						} else if key == "stealth" {
							if valBool, okB := item[1].(starlark.Bool); okB {
								stealth = bool(valBool)
							}
						}
					}
					return blocked, reason, redirect, silent, stealth
				}
			}
		}
	}
	return false, "", "", false, false
}

// OnPrivmsg triggers before a message is routed. If a string is returned by the starlark script, it modifies the msg.
// If None is returned by the script, it drops the message.
func (pm *PluginManager) OnPrivmsg(clientNick string, target string, message string) *string {
	currentMessage := message

	for _, plugin := range pm.plugins {
		if fn, ok := plugin.Globals["on_privmsg"]; ok {
			if starlarkFn, ok := fn.(starlark.Callable); ok {
				thread := pm.newThread("on_privmsg")
				res, err := starlark.Call(thread, starlarkFn, starlark.Tuple{starlark.String(clientNick), starlark.String(target), starlark.String(currentMessage)}, nil)
				if err != nil {
					pm.server.logger.Error("plugins", fmt.Sprintf("Error in %s on_privmsg", plugin.Name), err.Error())
					continue
				}

				if res == starlark.None {
					return nil // drop message
				} else if str, ok := res.(starlark.String); ok {
					currentMessage = str.GoString()
				}
			}
		}
	}
	return &currentMessage
}
