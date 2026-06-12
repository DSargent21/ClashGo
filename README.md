# ClashGO ⚔️

Hey there! This is **ClashGO**, a super lightweight, high-performance Clash of Clans bot written in Go. 

### Why did I make this? 
Honestly? I just wanted a bot that actually worked on my Mac. I tried using the original [MyBot.run](https://github.com/MyBotRun/MyBot) but it's built for Windows and uses AutoIt, which didn't really vibe with my setup. So, I spent 3 days vibe-coding this Go version from scratch. It's a bit rough around the edges, but it's fast, it's efficient, and it gets the job done.

---

## 🚀 Why Go? (The Nerd Stuff)

I chose Go because I wanted this thing to be as lightweight as possible while still being absolutely performative. 

- **Persistent ADB Transport**: Unlike other bots that spawn a new process for every single ADB command (which is slow and clunky), ClashGO keeps a persistent TCP connection open to the ADB server.
- **Screencaps in < 120ms**: Because of that persistent connection, we can grab screenshots and process them almost instantly.
- **Concurrency**: Go's goroutines make it super easy to handle the capture loop, state machine, and GUI all at once without breaking a sweat.
- **Single Binary**: No messy dependencies or runtime environments to install. Just build it and run it.

---

## 🛠️ Setup & Configuration

Setting up ClashGO is pretty straightforward. You just need an Android emulator (like BlueStacks or LDPlayer) and ADB enabled.

### 1. Requirements
- **Go 1.25+**: [Download here](https://go.dev/doc/install).
- **OpenCV 4.x**: Needed for the vision engine.
  ```bash
  brew install opencv pkg-config
  ```
- **ADB**: Ensure `adb` is in your PATH.

### 2. Emulator Setup
Set your emulator resolution to **860x732** with **160 DPI**. This is the sweet spot the bot is tuned for.

![Emulator Setup Placeholder](https://via.placeholder.com/600x400?text=Emulator+Resolution+860x732)

### 3. Config.json
The bot uses a `config.json` file to know where your emulator is and what your preferences are.

```json
{
  "device": {
    "adb_host": "127.0.0.1",
    "adb_port": 5037,
    "device_id": "localhost:5555"
  },
  "training": {
    "enabled": true,
    "full_army_before_attack": true
  },
  "attack": {
    "enabled": true,
    "strategy_file": "assets/strategies/auto_edrag_rush.yaml"
  }
}
```

---

## 💂 Armies & Training

ClashGO can automatically train your troops so you're always ready for the next raid.

### Selecting an Army
The bot uses **Quick Train** slots. You just need to have your preferred army saved in the game's Quick Train menu.

1. Open the Training menu in CoC.
2. Save your army to one of the Quick Train slots.
3. The bot will automatically detect if your army is full and use the Quick Train button to refill it after an attack.

![Quick Train Placeholder](https://via.placeholder.com/400x250?text=Quick+Train+Menu)

### Attack Strategies
Strategies are defined in `.yaml` files in `assets/strategies/`. You can pick which one to use in your `config.json`. 

Each strategy file tells the bot:
- Which troops to drop and where.
- When to use spells.
- When to activate Hero abilities (Queen, Warden, etc.).

Example strategy snippet:
```yaml
# auto_edrag_rush.yaml
- name: "Drop E-Drags"
  type: "troop"
  id: "electro_dragon"
  delay_after_ms: 500
```

---

## 🛠️ How to Help (Contribution)

This project is open-source because I want everyone to be able to use it, break it, and make it better. If you want to contribute:
- **Found a bug?** Open a **GitHub Issue**.
- **Have an improvement?** Send a **Pull Request (PR)**. This is the "professional" way to do it—I'll review your code, and if it's cool, I'll merge it in!

---

## 🏃 Running the Bot

### CLI Mode (Headless)
```bash
go build -tags cli -o clashgo .
./clashgo
```

### GUI Mode (Desktop)
```bash
wails dev
```

### Building for Release
```bash
make release
```
Artifacts land in `build/bin/ClashGO.dmg`.

---

## 📄 License
I switched this to the **MIT License**. Basically, do whatever you want with it. Just keep being awesome.
