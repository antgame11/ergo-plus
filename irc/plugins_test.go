package irc

import (
	"os"
	"testing"

	"github.com/ergochat/ergo/irc/logger"
)

func TestStarlarkPlugins(t *testing.T) {
	// Create a temporary plugins directory
	os.MkdirAll("plugins", 0755)
	defer os.RemoveAll("plugins")

	script := `
def on_join(nick, channel):
    print("Join: " + nick)

def on_part(nick, channel, reason):
    print("Part: " + nick + " from " + channel + " (" + reason + ")")

def on_quit(nick, reason):
    print("Quit: " + nick + " (" + reason + ")")

def on_kick(nick, channel, target, reason):
    print("Kick: " + nick + " kicked " + target + " from " + channel + " (" + reason + ")")

def on_nick(old_nick, new_nick):
    print("Nick: " + old_nick + " -> " + new_nick)

def on_connect(nick, ip):
    print("Connect: " + nick + " [" + ip + "]")

def on_validate_register(nick, username, hostname, ip):
    if nick == "admin":
        return "You cannot use the nickname 'admin'"
    return None

def on_validate_join(nick, channel):
    if channel == "#bad":
        return "This channel is forbidden"
    if channel == "#old":
        return {"redirect": "#new"}
    if channel == "#silent":
        return {"silent": True}
    if channel == "#stealth":
        return {"stealth": True}
    return None

def on_privmsg(nick, target, message):
    if "bad" in message:
        return None
    return message.upper()
`
	err := os.WriteFile("plugins/test.star", []byte(script), 0644)
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
		t.Fatalf("Expected 1 plugin, got %d", len(pm.plugins))
	}

	// Test OnJoin
	pm.OnJoin("alice", "#test")

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
}
