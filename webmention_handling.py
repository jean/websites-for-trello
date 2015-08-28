from trello import TrelloApi
from models import User, Board, List, Card, Label, Comment
from urlparse import urlparse
from sqlalchemy import func, select
from app import db
import os
import went
import requests

def handle_webmention(source, target):
    # find target card
    url = urlparse(target)
    pathsplit = filter(bool, url.path.split('/'))
    if url.netloc.endswith(os.environ['DOMAIN']):
        ok = Board.query.filter_by(subdomain=url.netloc.split('.')[0]).first()
    else:
        ok = len(db.engine.execute(select([func.preferences(url.netloc)])).first()[0].keys()) > 2
    if not ok or len(pathsplit) != 2:
        print ':: MODEL-UPDATES :: webmention targetting wrong place:', target
        return

    list = List.query.filter_by(slug=pathsplit[0]).first()
    if list:
        card = list.cards.filter_by(slug=pathsplit[1]).first()
    elif pathsplit[0] == 'c':
        card = Card.query.get(pathsplit[1])
    if not card:
        print ':: MODEL-UPDATES :: no card found for webmention:', target
        return

    # parse the webmention and get its contents
    try:
        webmention = went.Webmention(url=source, target=target)
    except went.NoContent:
        webmention = None
    if not webmention or not hasattr(webmention, 'body'):
        print ':: MODEL-UPDATES :: no webmention body or other problem at', source
        return

    raw = ":paperclip: **[{author_name}]({author_url})**\n\n{body}\n\non [{date}]({source_url}) via _[{source_display}]({source_url})_".format(
        author_name=webmention.author['name'].encode('utf-8'),
        author_url=webmention.author['url'],
        body='\n'.join(map(lambda l: '> ' + l, webmention.body.encode('utf-8').split('\n'))),
        date=webmention.date,
        source_url=webmention.url,
        source_display=getattr(webmention, 'via') or url
    )

    # check if it already exists
    comment = Comment.query.filter_by(card_id=card.id, source_url=webmention.url).first()

    if comment:
        # update comment
        r = requests.put('https://api.trello.com/1/actions/' + comment.id + '/text/', params={
            'key': os.environ['TRELLO_BOT_API_KEY'],
            'token': os.environ['TRELLO_BOT_TOKEN']
        }, data={'value': raw})
        r.raise_for_status()
    else:
        # create comment
        trello = TrelloApi(os.environ['TRELLO_BOT_API_KEY'], os.environ['TRELLO_BOT_TOKEN'])
        trello.cards.new_action_comment(card.id, unicode(raw, 'utf-8'))
