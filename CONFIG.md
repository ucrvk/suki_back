# Configuration

This project supports command-line flags and environment variables.

## Preferred environment variables

Use the `MAIDPIC_` prefix for all deployment settings:

- `MAIDPIC_SUPABASE_URL`
- `MAIDPIC_SUPABASE_API_KEY`
- `MAIDPIC_OUTPUT_DIR`
- `MAIDPIC_DB_PATH`
- `MAIDPIC_LISTEN_ADDR`
- `MAIDPIC_QUALITY`
- `MAIDPIC_SPEED`
- `MAIDPIC_UPDATE_NOW`
- `data/fcm_token.json` is the fixed Firebase service account file path.
- `FCM_PROXY` optionally sets the HTTP proxy used only by Firebase Admin SDK.

## Legacy compatibility

For backward compatibility, the app also accepts:

- `SUPABASE_URL`
- `SUPABASE_API_KEY`

## Flag mapping

- `-url` -> Supabase URL
- `-apikey` -> Supabase anon/public key
- `-out` -> output directory
- `-db` -> SQLite database path
- `-listen` -> Gin listen address
- `-quality` -> AVIF quality
- `-speed` -> AVIF speed
- `-u` -> run one maid image sync on startup

## Suggested defaults

- `MAIDPIC_SUPABASE_URL=https://uzlzkjuijruqanetagxh.supabase.co`
- `MAIDPIC_SUPABASE_API_KEY=...`
- `MAIDPIC_DB_PATH=./data/maid_pic.db`
- `MAIDPIC_OUTPUT_DIR=./data/pic`
- `MAIDPIC_LISTEN_ADDR=:6988`
