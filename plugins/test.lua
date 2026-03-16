-- Example ergo plugin demonstrating all available hooks and API functions.
-- Place .lua files in the plugins/ directory to load them at startup.
-- Use ergo.reload_plugins() (or the !reloadplugins command below) to reload.

local ergo = require("ergo")

-- on_joinyield: async observer for joins (does not block)
function on_joinyield(nick, channel)
end

-- on_joinintercept: sync join hook (legacy fallback: on_join)
function on_joinintercept(nick, channel)
end

-- on_partyield: async observer for parts
function on_partyield(nick, channel, reason)
end

-- on_partintercept: sync part hook (legacy fallback: on_part)
function on_partintercept(nick, channel, reason)
end

-- on_quityield: async observer for quits
function on_quityield(nick, reason)
end

-- on_quitintercept: sync quit hook (legacy fallback: on_quit)
function on_quitintercept(nick, reason)
end

-- on_kickyield: async observer for kicks
function on_kickyield(kicker, channel, target, reason)
end

-- on_kickintercept: sync kick hook (legacy fallback: on_kick)
function on_kickintercept(kicker, channel, target, reason)
end

-- on_nickyield: async observer for nick changes
function on_nickyield(old_nick, new_nick)
end

-- on_nickintercept: sync nick hook (legacy fallback: on_nick)
function on_nickintercept(old_nick, new_nick)
end

-- on_connectyield: async observer for connects
function on_connectyield(nick, ip)
end

-- on_connectintercept: sync connect hook (legacy fallback: on_connect)
function on_connectintercept(nick, ip)
end

-- on_validate_registeryield: async observer for registration attempts
function on_validate_registeryield(nick, username, hostname, ip)
	-- metrics/audit side effects only
end

-- on_validate_registerintercept: sync registration validation
-- (legacy fallback: on_validate_register)
-- Return a string to block with that reason, or nil to allow.
function on_validate_registerintercept(nick, username, hostname, ip)
	local forbidden = {"spam", "abuse", "admin"}
	for _, word in ipairs(forbidden) do
		if nick:lower() == word then
			return "Nickname '" .. nick .. "' is not allowed"
		end
	end
end

-- on_validate_joinyield: async observer for join validation checks
function on_validate_joinyield(nick, channel)
end

-- on_validate_joinintercept: sync join validation
-- (legacy fallback: on_validate_join)
-- Return a string to block, a table for structured control, or nil to allow.
-- Table keys: block (string), redirect (string), silent (bool), stealth (bool)
function on_validate_joinintercept(nick, channel)
end

-- on_privmsgyield: async observer for every PRIVMSG.
-- This runs in the background and cannot intercept the current message path.
function on_privmsgyield(nick, target, message)
	if message == "!seen" then
		ergo.save_state("last_seen_message", message)
	end
end

-- on_privmsgintercept: sync interceptor for every PRIVMSG.
-- Return nil to drop the message, or return (possibly modified) message text.
-- Returning nothing also drops the message — always return message to pass through.
function on_privmsgintercept(nick, target, message)
	if message == "!hits" then
		local hits = ergo.load_state("hits") or 0
		hits = hits + 1
		ergo.save_state("hits", hits)
		ergo.send_message(nick, "Total hits: " .. tostring(hits))
		return message
	end

	if message == "!time" then
		local t = ergo.time_now()
		ergo.send_message(nick, "Server time: " .. ergo.time_format("2006-01-02 15:04:05", t))
		return message
	end

	if message == "!reloadplugins" then
		ergo.reload_plugins()
		ergo.send_message(nick, "Plugins reloaded.")
		return message
	end

	return message
end
