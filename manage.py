import os
import redis
import datetime

from flask.ext.script import Manager
from flask.ext.migrate import Migrate, MigrateCommand

from app import app, db, redis
from models import *

manager = Manager(app)

@manager.command
def stats():
    pvkeys = redis.keys('pageviews:%s:%s:*' % (datetime.date.today().year, datetime.date.today().month))
    pvvalues = redis.mget(pvkeys)
    whkeys = redis.keys('webhooks:%s:%s:*' % (datetime.date.today().year, datetime.date.today().month))
    whvalues = redis.mget(whkeys)

    _, year, month, _ = pvkeys[0].split(':')
    pvids = [k.split(':')[-1] for k in pvkeys]
    whids = [k.split(':')[-1] for k in whkeys]
    ids = set(pvids + whids)
    pv = dict(zip(pvids, map(int, pvvalues)))
    wh = dict(zip(whids, map(int, whvalues)))
    boards = {}
    for id, pvv in pv.items():
        boards[id] = (pvv, 0, id)
    for id, whv in wh.items():
        t = boards.get('id') or (0, 0, id)
        t = (t[0], whv, id)
        boards[id] = t

    print '%7s %24s %27s %5s %5s' % ('month', 'subdomain', 'user', 'PV', 'WH')
    for pvv, whv, id in sorted(boards.values()):
        board = Board.query.get(id)
        if board:
            print '%2s/%s %24s %27s %5d %5d' % (month, year, board.subdomain, board.user_id, pvv, whv)
        else:
            print '%2s/%s %52s %5d %5d' % (month, year, id, pvv, whv)

# flask-migrate
Migrate(app, db)
manager.add_command('db', MigrateCommand)

if __name__ == "__main__":
    manager.run()
