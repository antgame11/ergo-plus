# Starlark Plugin Sample

# Called when a user joins a channel
def on_join(nick, channel):
    print("User %s joined %s" % (nick, channel))
    if channel == "#lobby":
        send_message(nick, "Welcome to the server lobby, %s!" % nick)

# Called before a user joins. 
# Return a string to block with that reason.
# Return a dict {"redirect": "#newchan"} to redirect.
def on_validate_join(nick, channel):
    if channel == "#forbidden":
        return "This channel is private"
    
    if channel == "#old-lobby":
        return {"redirect": "#lobby"}

    # Make joins to #secret silent (no JOIN message for others)
    # and send a custom notice instead
    if channel == "#secret":
        send_notice("#secret", "✨ Someone stepped into the darkness...")
        return {"silent": True}
    
    # Stealth join: no JOIN message AND hidden from NAMES/WHO
    if channel == "#stealth":
        return {"stealth": True}
    
    return None

# Called when a user parts a channel
def on_part(nick, channel, reason):
    print("User %s parted %s (%s)" % (nick, channel, reason))

# Called when a user quits the server
def on_quit(nick, reason):
    print("User %s quit (%s)" % (nick, reason))

# Called when a user is kicked from a channel
def on_kick(nick, channel, target, reason):
    print("User %s kicked %s from %s (%s)" % (nick, target, channel, reason))

# Called when a user changes their nickname
def on_nick(old_nick, new_nick):
    print("User %s changed nick to %s" % (old_nick, new_nick))
    

# Called during registration. Return a string to block the login.
def on_validate_register(nick, username, hostname, ip):
    print("Validating registration: %s" % nick)
    bad_words = ["slur1", "slur2"] # example
    for word in bad_words:
        if word in nick.lower():
            return "Nickname contains a forbidden word"
    
    if nick.lower() == "admin":
        return "You cannot use the nickname 'admin'"

# Called when a user finishes registration/connects
def on_connect(nick, ip):
    print("User %s connected from %s" % (nick, ip))

# Called before a PRIVMSG/NOTICE is sent
def on_privmsg(nick, target, message):
    if "fart" in message.lower():
        return message.replace("fart", "[censored]")
    
    # Persistence example: !hits
    if message == "!hits":
        hits = load_state("hits")
        if hits == None:
            hits = 0
        hits += 1
        save_state("hits", hits)
        send_notice(target, "Command !hits has been called " + str(hits) + " times (persisted).")
        return None

    # Async HTTP example: !joke
    if message == "!joke":
        def joke_callback(body, status, error, userdata):
            if error:
                send_notice(userdata["target"], "Error fetching joke: " + error)
            elif status == 200:
                data = json_parse(body)
                send_notice(userdata["target"], "Joke: " + data.get("joke", "No joke found?"))
            else:
                send_notice(userdata["target"], "Unexpected HTTP status: " + str(status))
        
        http_get("https://v2.jokeapi.dev/joke/Any?type=single", joke_callback, userdata={"target": target})
        return None

    # Time example: !time
    if message == "!time":
        now = time_now()
        formatted = time_format("2006-01-02 15:04:05", now)
        send_notice(target, "Current time: " + formatted)
        return None

    # Reload plugins: !reloadplugins
    if message == "!reloadplugins":
        if has_role_capab(nick, "admin"):
            send_notice(target, "Reloading plugins...")
            reload_plugins()
        else:
            send_notice(nick, "Error: You need the 'admin' role capability.")
        return None
            
    if message.startswith("!info"):
        client = get_client(nick)
        if client:
            send_message(nick, "Your hostname is: " + client["hostname"])
            return None
            
    return message
