# Telegram Bot Setup (Customer Guide)

Use this to enable the PayRam Telegram bot so teammates can ask PayRam questions directly in Telegram.

## Prerequisites
- OpenAI API key (so responses can be generated)
- Telegram bot token from @BotFather
- PayRam dashboard login (email/password)
- Docker installed (recommended path)

## First-time setup
1) Run the guided installer:
   ```
   ./setup_payram_agent.sh
   ```
2) Answer the prompts (the script validates your dashboard login):
   - PayRam server URL (http/https)
   - PayRam dashboard admin email + password
   - OpenAI API key (can be empty)
  - Telegram bot token (from @BotFather)
  - Telegram usernames to allow (comma-separated, `user1,@user2,t.me/user3` all accepted)
   - Auto-update choice

## Fast allowlist updates (no full re-run)
- Append usernames (preferred):
  ```sh
  ./setup_payram_agent.sh --add-telegram-usernames "alice,@bob,t.me/carol"
  ```
- Append chat IDs (For telegram groups):
  ```sh
  ./setup_payram_agent.sh --add-telegram-chat-ids "12345,67890"
  ```

### Groups
- Groups do not have usernames. Add the group chat ID via the chat-ID fast path:
  ```sh
  ./setup_payram_agent.sh --add-telegram-chat-ids "<group_chat_id>"
  ```

## Get your chat ID quickly
If you message the bot from a chat that is not allowlisted, it will reply with an "Access denied" notice that includes the `chat_id` to add. Add that chat ID with the fast path above, then retry your message.

## Using the bot
1) From an allowlisted chat, send any question to your bot.
2) The bot replies with PayRam answers in the same chat.

What you can ask (examples):
- "Show me today’s payments summary"
- "Create payments link for 3USD, on main project with email: example@gmail.com, and customerid: cust-123"
- "Top paying users this week"
- "Deposit distribution by chain for last 7 days"
- "Payouts by currency for December"
- "User growth vs previous period"

If you don’t get a reply:
- Make sure your chat ID is allowlisted.
- Confirm the bot token is correct (set during setup).
- Restart the `payram-analytics-agent` container and try again.

## Quick tips
- Keep the container ports bound to `127.0.0.1 (localhost)`.
