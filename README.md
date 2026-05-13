# TDLib Server

**TDLib Server** is a high-performance Go server built to scale Telegram bots using [TDLib](https://github.com/tdlib/td) and [NATS](https://nats.io).

## Table of Contents

- [Features](#features)
- [Usage](#usage)
- [Installation](#installation)
- [Configuration](#configuration)
- [Acknowledgments](#acknowledgments)
- [License](#license)

<a name="features"></a>

## Features

- **Easy to Use**: Simple setup and configuration for quick integration.
- **High Performance**: Optimized for scaling high-loaded Telegram bots.
- **Go-Powered**: Built with Go for concurrency and speed.
- **NATS Integration**: Fast and lightweight messaging with NATS for efficient asynchronous communication.

<a name="usage"></a>

## Usage

Here’s an example of how you can use [Pytdbot](https://github.com/pytdbot/client) to interact with **TDLib Server**:

```python
import asyncio
import logging
from pytdbot import Client
from pytdbot.types import Message

logging.basicConfig(
    level=logging.INFO,
    format="[%(levelname)s][%(filename)s:%(lineno)d][%(funcName)s] %(message)s",
)

client = Client(
    token="508903:AAGyYP5y63ihh_7KFX9aNiaHfajAmcAA",
    nats_url="nats://127.0.0.1:4222",  # NATS server URL
)


@client.on_message()
async def say_hello(c: Client, message: Message):
    msg = await message.reply_text("Hey there! I'm cooking up a surprise... 🍳👨‍🍳")

    async with message.action("choose_sticker"):
        await asyncio.sleep(5)

        await msg.edit_text("Boo! 👻 Just kidding.")

asyncio.run(client.run())
```

<a name="installation"></a>

## Installation

Follow these steps to set up and build **TDLib Server** on your system.

### Requirements

- **Go >= 1.21**
- **[TDLib](https://github.com/tdlib/td#building)**
- **[NATS Server](https://docs.nats.io/running-a-nats-service/introduction/installation)**

Once TDLib and NATS are installed, you're ready to build **TDLib Server**:

- Clone the repository

    ```bash
    git clone https://github.com/pytdbot/tdlib-server
    cd tdlib-server
    ```

- Build TDLib Server
    - If **TDLib is not installed system-wide** (a.k.a `/usr/local`):

        ```bash
        TDLIB_DIR="/path/to/tdlib" make build
        ```

        Ensure you adjust the paths to match your installation directories.

        By default, TDLib installs files at `td/tdlib`.

    - If **TDLib is installed system-wide** (recommended), just run:

        ```bash
        make build
        ```

    - Optionally, you can install `tdlib-server` system-wide:
        ```bash
        make install
        ```

- Run NATS Server:

    ```bash
    nats-server -a 127.0.0.1
    ```

- Run TDLib Server:
    ```bash
    tdlib-server --config config.ini
    ```

<a name="configuration"></a>

## Configuration

All configuration options can be found in [config.ini](config.ini).

You can also view the available CLI options by running:

```bash
tdlib-server --help
```

<a name="acknowledgments"></a>

## Acknowledgments

- Thank you for taking the time to view or use this project.

- Special thanks to [@levlam](https://github.com/levlam) for maintaining [TDLib](https://github.com/tdlib/td) and for his support in creating [TDLib Server](https://github.com/pytdbot/tdlib-server).

<a name="license"></a>

## License

This project is licensed under the [MIT License](LICENSE).
