"""断网队列重启后继续上报，只有服务器确认的记录才移除。"""
from pathlib import Path
import tempfile
import time
import unittest
import uuid

from connector import RecordQueue


class ConnectorQueueTests(unittest.TestCase):
    def test_queue_survives_restart_and_acknowledges_only_confirmed_records(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'records.sqlite3'
            first = {'event_key': uuid.uuid4().hex, 'ts': int(time.time()), 'domain': 'example.org'}
            second = first | {'event_key': uuid.uuid4().hex}
            queue = RecordQueue(path)
            queue.append(first)
            queue.append(first)
            queue.append(second)
            queue = RecordQueue(path)
            self.assertEqual(queue.batch(), [first, second])
            queue.acknowledge([first['event_key']])
            self.assertEqual(RecordQueue(path).batch(), [second])
            queue.append(first | {'event_key': uuid.uuid4().hex, 'ts': int(time.time()) - 8 * 86400})
            self.assertEqual(queue.batch(), [second])
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
