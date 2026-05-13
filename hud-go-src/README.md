# How to generate new db migration files
# It deletes and recreates the migrations/ directory each time
# so it always reflects the current state of tsw_hud.db.
cd c:/Users/hcfai/Desktop/applications/hud-go
python generate_migrations.py

# Rebuild the db from the migration files
# Using Python (no sqlite3 CLI needed):
cd migrations
python rebuild.py

# Or with sqlite3 CLI:
bash rebuild.sh


# Rebuild the binary
go build -o tsw-hud.exe .  

# Run the server
 .\tsw-hud.exe 


 # Run without Subscriptions
 # Used when scrapping data with the bot
 .\tsw-hud.exe --no-subscriptions  