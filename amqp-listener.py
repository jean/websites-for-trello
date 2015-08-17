import os
import sys
import pika
import math
import time
import json
import signal
import shelve
import requests
import datetime
import traceback
import handlers as h
import redis.exceptions as redis_exceptions
from raygun4py import raygunprovider
from board_management import board_setup, add_bot, remove_bot
from initial_fetch import initial_fetch
from webmention_handling import handle_webmention
from models import Board
from app import app, redis

class LocalTimeUp(BaseException):
    pass

class MessageCount(BaseException):
    pass

pwd = os.path.dirname(os.path.realpath(__file__))
raygun = raygunprovider.RaygunSender(os.environ['RAYGUN_API_KEY'])
counts = shelve.open(os.path.join(pwd, 'counts.store'))
starttime = datetime.datetime.now()

# open connection to cloudamqp
params = pika.URLParameters(os.environ['CLOUDAMQP_URL'])
params.socket_timeout = 5
connection = pika.BlockingConnection(params)
channel = connection.channel()
channel.queue_declare(queue='wft', durable=True)

def main():
    print ':: MODEL-UPDATES :: waiting for messages.'

    # start listening
    listen()

def listen():
    # this is where we actually start listening
    messages = []
    try:
        # local alarm, 20 seconds
        def proceed(signum, frame):
            raise LocalTimeUp('proceed.')
        signal.signal(signal.SIGALRM, proceed)
        signal.alarm(15)

        for method, properties, body in channel.consume(queue='wft'):
            print ':: MODEL-UPDATES :: got a message. now we have %s.' % (len(messages) + 1)
            try:
                messages.append((json.loads(body), method))
            except ValueError:
                print ':: MODEL-UPDATES :: non-json message:', body
                channel.basic_ack(delivery_tag=method.delivery_tag)

            if len(messages) == 10:
                print ':: MODEL-UPDATES :: got 10, will process.'
                signal.alarm(0)
                raise MessageCount

    except (LocalTimeUp, MessageCount):
        if messages:
            process_message_batch(messages)

        if (datetime.datetime.now() - starttime).seconds > 170:
            # running for more than 170 seconds. stop.
            print ':: MODEL-UPDATES :: end of time.'
            channel.cancel()
            connection.close()
            sys.exit()
        else:
            # not running for enough time yet. restart.
            listen()

def process_message_batch(messages):
    sorted_messages = sorted(messages, key=lambda m: m[0].get('date'))

    #print ':: MODEL-UPDATES :: sorted/unsorted/type'
    #for i in range(len(sorted_messages)):
    #    print '\t{} | {} | {}'.format(payloads[i].get('date'), s[i].get('date'), s[i].get('type'))

    for message, method in sorted_messages:
        payload = message.get('action') or message
        with app.app_context():
            try:
                process_message(payload)
            except Exception:
                print ':: MODEL-UPDATES :: payload:', payload
                traceback.print_exc(file=sys.stdout)

        # delete message frm rabbitmq
        channel.basic_ack(delivery_tag=method.delivery_tag)

def process_message(payload):
    print ':: MODEL-UPDATES :: processing', payload.get('date'), payload.get('type')

    if payload['type'] == 'boardSetup':
        board_id = str(payload['board_id'])
        counts[board_id] = 0

        try:
            add_bot(payload['user_token'], payload['board_id'])
            initial_fetch(payload['board_id'], username=payload['username'], user_token=payload['user_token'])
            board_setup(payload['board_id'])
        except:
            raygun.set_user(payload['username'])
            raygun.send_exception(
                exc_info=sys.exc_info(),
                userCustomData={'board_id': payload['board_id']},
                tags=['boardSetup']
            )
            traceback.print_exc(file=sys.stdout)
            print ':: MODEL-UPDATES :: payload:', payload

        counts[board_id] = 0

    elif payload['type'] == 'initialFetch':
        try:
            initial_fetch(payload['board_id'])
        except:
            raygun.send_exception(
                exc_info=sys.exc_info(),
                userCustomData={'board_id': payload['board_id']},
                tags=['initialFetch']
            )
            traceback.print_exc(file=sys.stdout)

    elif payload['type'] == 'boardDeleted':
        board_id = str(payload['board_id'])
        del counts[board_id]

        try:
            remove_bot(payload['board_id'])
        except:
            raygun.set_user(payload['username'])
            raygun.send_exception(
                exc_info=sys.exc_info(),
                userCustomData={'board_id': payload['board_id']},
                tags=['boardDeleted']
            )
            traceback.print_exc(file=sys.stdout)
            print ':: MODEL-UPDATES :: payload:', payload

    elif payload['type'] == 'webmentionReceived':
        try:
            handle_webmention(source=payload['source'], target=payload['target'])
        except:
            raygun.send_exception(
                exc_info=sys.exc_info(),
                userCustomData=payload,
                tags=['webmentionReceived']
            )
            traceback.print_exc(file=sys.stdout)
            print ':: MODEL-UPDATES :: payload:', payload

    else:
        board_id = str(payload['data']['board']['id'])

        try:
            handler = getattr(h, payload['type'])
            handler(payload['data'], payload=payload)
        except AttributeError:
            return
        except:
            if not Board.query.get(payload['data']['board']['id']):
                print ':: MODEL-UPDATES :: webhook for a board not registered anymore.'
            raygun.set_user(payload['memberCreator']['username'])
            raygun.send_exception(
                exc_info=sys.exc_info(),
                userCustomData=payload['data'],
                tags=['webhook', payload['type']]
            )
            traceback.print_exc(file=sys.stdout)
            print ':: MODEL-UPDATES :: payload:', payload

        # count webhooks on redis
        today = datetime.date.today()
        try:
            redis.incr('webhooks:%d:%d:%s' % (today.year, today.month, payload['data']['board']['id']))
        except redis_exceptions.ResponseError as e:
            print ':: MODEL-UPDATES ::', e, ' -- couldn\'t INCR webhooks:%d:%d:%s' % (today.year, today.month, payload['data']['board']['id'])

        # count up for this board. every x messages we do a initial-fetch
        counts[board_id] = counts.get(board_id, 0) + 1
        thiscount = counts[board_id]
        exponent = 3
        while True:
            divisor = 2**exponent
            print '2 **',exponent, '|', divisor, '/', thiscount
            if exponent > 25:
                break # just for safety
            elif divisor == thiscount:
                initial_fetch(board_id)
                break
            elif divisor > thiscount:
                exponent += 1
            elif divisor < thiscount:
                break

if __name__ == '__main__':
    main()

# this is meant to be run as a cron job every 3 minutes or so.
