package irc

import (
	"os"
	"testing"
	"time"

	"github.com/ergochat/ergo/irc/logger"
)

func TestLuaPlugins(t *testing.T) {
	// Create a temporary plugins directory
	os.MkdirAll("plugins", 0755)
	defer os.RemoveAll("plugins")

	script := `
local ergo = require("ergo")

function on_join(nick, channel) end
function on_joinyield(nick, channel)
	ergo.save_state("join_yield_last", nick .. "|" .. channel)
end
function on_joinintercept(nick, channel)
	ergo.save_state("join_intercept_last", nick .. "|" .. channel)
end
function on_part(nick, channel, reason) end
function on_quit(nick, reason) end
function on_kick(nick, channel, target, reason) end
function on_nick(old_nick, new_nick) end
function on_connect(nick, ip) end

function on_validate_registeryield(nick, username, hostname, ip)
	ergo.save_state("register_yield_last", nick)
end

function on_validate_registerintercept(nick, username, hostname, ip)
	if nick == "admin" then
		return "You cannot use the nickname 'admin'"
	end
end

function on_validate_joinyield(nick, channel)
	ergo.save_state("join_validate_yield_last", channel)
end

function on_validate_joinintercept(nick, channel)
	if channel == "#bad" then
		return "This channel is forbidden"
	end
	if channel == "#old" then
		return {redirect = "#new"}
	end
	if channel == "#silent" then
		return {silent = true}
	end
	if channel == "#stealth" then
		return {stealth = true}
	end
end

function on_privmsgyield(nick, target, message)
	if message == "observe me" then
		ergo.save_state("yield_last", message)
	end
end

function on_privmsgintercept(nick, target, message)
	if string.find(message, "bad") then
		return nil
	end
	return string.upper(message)
end
`
	err := os.WriteFile("plugins/test.lua", []byte(script), 0644)
	if err != nil {
		t.Fatal(err)
	}

	logManager, _ := logger.NewManager(nil)
	server := &Server{
		logger: logManager,
	}
	server.clients.Initialize()

	pm := NewPluginManager(server)
	if len(pm.plugins) != 1 {
		_, loadErr := pm.loadPlugin("plugins/test.lua", "test.lua")
		if loadErr != nil {
			t.Fatalf("Expected 1 plugin, got %d: %v", len(pm.plugins), loadErr)
		}
		t.Fatalf("Expected 1 plugin, got %d", len(pm.plugins))
	}

	// Test OnJoin
	pm.OnJoin("alice", "#test")
	pm.stateMutex.RLock()
	joinIntercept, joinInterceptOK := pm.state["join_intercept_last"]
	pm.stateMutex.RUnlock()
	if !joinInterceptOK || joinIntercept != "alice|#test" {
		t.Errorf("Expected join_intercept_last to be alice|#test, got %v", joinIntercept)
	}

	joinYieldDeadline := time.Now().Add(500 * time.Millisecond)
	joinYieldSeen := false
	for time.Now().Before(joinYieldDeadline) {
		pm.stateMutex.RLock()
		value, ok := pm.state["join_yield_last"]
		pm.stateMutex.RUnlock()
		if ok {
			if str, ok := value.(string); ok && str == "alice|#test" {
				joinYieldSeen = true
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !joinYieldSeen {
		t.Errorf("Expected on_joinyield to set join_yield_last state")
	}

	// Test OnPart
	pm.OnPart("alice", "#test", "leaving")

	// Test OnQuit
	pm.OnQuit("alice", "goodbye")

	// Test OnKick
	pm.OnKick("bob", "#test", "alice", "bad behavior")

	// Test OnNick
	pm.OnNick("alice", "alicia")

	// Test OnConnect
	pm.OnConnect("alice", "127.0.0.1")

	// Test OnValidateRegister block
	blockRes := pm.OnValidateRegister("admin", "admin", "localhost", "127.0.0.1")
	if blockRes == nil || *blockRes != "You cannot use the nickname 'admin'" {
		t.Errorf("Expected block reason, got %v", blockRes)
	}

	registerYieldDeadline := time.Now().Add(500 * time.Millisecond)
	registerYieldSeen := false
	for time.Now().Before(registerYieldDeadline) {
		pm.stateMutex.RLock()
		value, ok := pm.state["register_yield_last"]
		pm.stateMutex.RUnlock()
		if ok {
			if str, ok := value.(string); ok && str == "admin" {
				registerYieldSeen = true
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !registerYieldSeen {
		t.Errorf("Expected on_validate_registeryield to set register_yield_last state")
	}

	// Test OnValidateRegister allow
	allowRes := pm.OnValidateRegister("alice", "alice", "localhost", "127.0.0.1")
	if allowRes != nil {
		t.Errorf("Expected nil (allow), got %v", *allowRes)
	}

	// Test OnValidateJoin block
	blocked, reason, redirected, silent, stealth := pm.OnValidateJoin("alice", "#bad")
	if !blocked || reason != "This channel is forbidden" {
		t.Errorf("Expected block, got %v, %v", blocked, reason)
	}

	validateYieldDeadline := time.Now().Add(500 * time.Millisecond)
	validateYieldSeen := false
	for time.Now().Before(validateYieldDeadline) {
		pm.stateMutex.RLock()
		value, ok := pm.state["join_validate_yield_last"]
		pm.stateMutex.RUnlock()
		if ok {
			if str, ok := value.(string); ok && str == "#bad" {
				validateYieldSeen = true
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !validateYieldSeen {
		t.Errorf("Expected on_validate_joinyield to set join_validate_yield_last state")
	}

	// Test OnValidateJoin redirect
	blocked, reason, redirected, silent, stealth = pm.OnValidateJoin("alice", "#old")
	if blocked || redirected != "#new" {
		t.Errorf("Expected redirect to #new, got %v, %v, %v", blocked, reason, redirected)
	}

	// Test OnValidateJoin silent
	blocked, reason, redirected, silent, stealth = pm.OnValidateJoin("alice", "#silent")
	if blocked || !silent {
		t.Errorf("Expected silent, got %v, %v", blocked, silent)
	}

	// Test OnValidateJoin stealth
	blocked, reason, redirected, silent, stealth = pm.OnValidateJoin("alice", "#stealth")
	if blocked || !stealth {
		t.Errorf("Expected stealth, got %v, %v", blocked, stealth)
	}

	// Test OnValidateJoin allow
	blocked, reason, redirected, silent, stealth = pm.OnValidateJoin("alice", "#cool")
	if blocked || redirected != "" || silent || stealth {
		t.Errorf("Expected allow, got %v, %v, %v, %v, %v", blocked, reason, redirected, silent, stealth)
	}

	// Test OnPrivmsg modification
	msg := "hello"
	mod := pm.OnPrivmsg("alice", "#test", msg)
	if mod == nil || *mod != "HELLO" {
		t.Errorf("Expected HELLO, got %v", mod)
	}

	// Test OnPrivmsg drop
	msg2 := "this is bad"
	mod2 := pm.OnPrivmsg("alice", "#test", msg2)
	if mod2 != nil {
		t.Errorf("Expected nil (dropped), got %v", *mod2)
	}

	// Test async yield hook plus intercept hook
	msg3 := "observe me"
	mod3 := pm.OnPrivmsg("alice", "#test", msg3)
	if mod3 == nil || *mod3 != "OBSERVE ME" {
		t.Errorf("Expected OBSERVE ME, got %v", mod3)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	yieldSeen := false
	for time.Now().Before(deadline) {
		pm.stateMutex.RLock()
		value, ok := pm.state["yield_last"]
		pm.stateMutex.RUnlock()
		if ok {
			if str, ok := value.(string); ok && str == "observe me" {
				yieldSeen = true
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !yieldSeen {
		t.Errorf("Expected on_privmsgyield to set yield_last state")
	}
}
