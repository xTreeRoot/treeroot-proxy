"""Consistent SQLite backup; run daily as the treeroot service user."""
from datetime import datetime, timezone
from pathlib import Path
import sqlite3

root = Path('/var/lib/treeroot')
folder = root / 'backups'
folder.mkdir(mode=0o700, exist_ok=True)
target = folder / ('panel-' + datetime.now(timezone.utc).strftime('%Y%m%d') + '.sqlite3')
with sqlite3.connect(root / 'panel.sqlite3') as source, sqlite3.connect(target) as destination:
    source.backup(destination)
target.chmod(0o600)
for old in sorted(folder.glob('panel-*.sqlite3'))[:-7]:
    old.unlink()
