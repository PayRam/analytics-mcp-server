# Telegram Bot Setup (Customer Guide)

Use this to enable the PayRam Telegram bot so teammates can ask PayRam questions directly in Telegram.

## Prerequisites
- OpenAI API key (so responses can be generated)
- Telegram bot token from @BotFather
- PayRam dashboard login (email/password)
- Docker installed (recommended path)

## First-time setup
1) Run the guided installer (Telegram is enabled by default):
   ```
   ./setup_payram_agent.sh
   ```
2) Provide the prompts you see:
   - PayRam server URL
   - PayRam dashboard email + password
   - OpenAI API key (you can leave empty and set later)
   - Telegram bot token
   - Telegram chat IDs that are allowed to use the bot (you can add later)
   - Auto-update choice
3) Start the container with the `docker run` command shown at the end. The bot runs inside the `payram-analytics-agent` container.

## Get your chat ID quickly
If you message the bot from a chat that is not allowlisted, it will reply with an "Access denied" notice that includes the `chat_id` to add. Add it without rerunning setup:
```sh
./setup_payram_agent.sh --add-telegram-chat-ids "<chat_id_from_message>"
```
Retry your message after adding the chat.

## Add Telegram chat IDs later (no full re-setup)
If the bot is already running and you just need to allow more chats:
```sh
./setup_payram_agent.sh --add-telegram-chat-ids "12345,67890"
```
The script updates the allowlist and offers to restart the running container so changes take effect.

## Using the bot
1) From an allowlisted chat, send any question to your bot.
2) The bot replies with PayRam answers in the same chat.

If you don’t get a reply:
- Make sure your chat ID is allowlisted.
- Confirm the bot token is correct (set during setup).
- Restart the `payram-analytics-agent` container and try again.

## Quick tips
- Keep the container ports bound to `127.0.0.1` or place behind your own network controls.
