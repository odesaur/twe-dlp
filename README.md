![flowerr](./flowerr.png) 

`A CLI tool that downloads every Twitch emote from a channel.`

### Disclaimer
Emote and badge images are property of Twitch Interactive or their
respective owners. Do not reuse without obtaining their permission.

### Usage

Run with a Twitch username:

```bash
./twe-dlp <username>
```

Run with a Twitch channel ID:

```bash
./twe-dlp <userid>
```

Run with no arguments (interactive prompt):

```bash
./twe-dlp
Enter Twitch channel name or numeric ID:
```

The program creates a folder named after the channel (e.g., `username/`) and saves all emotes inside it.

### Install using `go install`

You can install the tool directly from GitHub:

```bash
go install github.com/odesaur/twe-dlp@latest
```

This places a compiled binary into:
```
$GOPATH/bin
```
Make sure that path is in your `$PATH` environment variable.

### Build locally

Clone the repository:

```bash
git clone https://github.com/odesaur/twe-dlp
cd twe-dlp
```

Build:

```bash
go build -o twe-dlp
```
