#!/usr/bin/env python3
"""One owner HTTP process; original FastMCP globals and SDK per logical session.

Only initialization allocates an actor. SDK lifecycle is owned by that actor,
so explicit close also releases its SDK transport bookkeeping. No stdio, SDK
patch, automatic action replay, or eviction of a live/uncertain session.
"""
from __future__ import annotations

import argparse
import asyncio
from contextlib import asynccontextmanager
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import stat
import sys
import time
import uuid

from python_mcp_http import OwnerHTTP, load_binding


class Actors:
    def __init__(self, maximum=16):
        if type(maximum) is not int or not 1 <= maximum <= 16:
            raise ValueError('bounded actor count required')
        self.maximum=maximum
        self.items=set()

    def add(self, actor):
        if len(self.items)>=self.maximum:
            raise ValueError('session capacity reached; close an owned session')
        self.items.add(actor)

    def discard(self, actor):
        self.items.discard(actor)


def load_original(config, source):
    name='_gptadmin_session_mcp_'+uuid.uuid4().hex
    path=Path(config['module'])
    spec=importlib.util.spec_from_loader(name,loader=None,origin=str(path))
    module=importlib.util.module_from_spec(spec)
    module.__file__=str(path)
    sys.modules[name]=module
    try:
        exec(compile(source,str(path),'exec'),module.__dict__)
        from mcp.server.fastmcp import FastMCP
        if not isinstance(module.mcp,FastMCP):
            raise ValueError('original native FastMCP export required')
        return module
    except BaseException:
        sys.modules.pop(name,None)
        raise


class Actor:
    def __init__(self, create_module, tool_lock):
        # No module/SDK allocation until the router reserves the actor slot.
        self.create_module=create_module
        self.tool_lock=tool_lock
        self.sid=None
        self.http=0
        self.streams=0
        self.callbacks=0
        self.uncertain=False
        self.closing=False
        self.last=time.monotonic()
        self.ready=asyncio.Event()
        self.stop=asyncio.Event()
        self.task=None
        self.removal=None
        self.manager=None
        self.module=None

    async def run(self):
        from mcp.server.lowlevel import Server
        from mcp.server.streamable_http_manager import StreamableHTTPSessionManager
        module=None
        try:
            module=self.create_module()
            self.module=module
            native=module.mcp
            server=Server(native.name,instructions=native.instructions)
            @asynccontextmanager
            async def callback():
                self.callbacks+=1
                try:yield
                finally:self.callbacks-=1;self.last=time.monotonic()
            @server.list_tools()
            async def list_tools():return await native.list_tools()
            @server.call_tool()
            async def call_tool(name,arguments):
                async with callback():
                    async with self.tool_lock:return await native.call_tool(name,arguments)
            @server.list_resources()
            async def list_resources():return await native.list_resources()
            @server.list_resource_templates()
            async def list_templates():return await native.list_resource_templates()
            @server.read_resource()
            async def read_resource(uri):
                async with callback():return await native.read_resource(str(uri))
            @server.list_prompts()
            async def list_prompts():return await native.list_prompts()
            @server.get_prompt()
            async def get_prompt(name,arguments):
                async with callback():return await native.get_prompt(name,arguments)
            self.manager=StreamableHTTPSessionManager(server,json_response=True,stateless=False)
            # Enter/exit SDK task group on this SAME task. Clearing this owned
            # manager releases even its terminated-transport bookkeeping.
            async with self.manager.run():
                self.ready.set()
                await self.stop.wait()
        finally:
            self.ready.set()
            if module is not None:sys.modules.pop(module.__name__,None)
            self.module=None

    async def close(self):
        self.stop.set()
        if self.task is not None:await self.task


class SessionRouter:
    def __init__(self, create_module, maximum=16, idle_seconds=1800):
        self.create_module=create_module
        self.actors=Actors(maximum)
        self.by_id={}
        self.tool_lock=asyncio.Lock()
        self.requests=asyncio.Semaphore(2)
        self.idle_seconds=idle_seconds
        self.stopping=False

    async def reply(self,send,status):
        await send({'type':'http.response.start','status':status,
                    'headers':[(b'content-length',b'0'),(b'cache-control',b'no-store')]})
        await send({'type':'http.response.body','body':b''})

    async def initialize_body(self, receive):
        parts=[];total=0
        while True:
            m=await receive()
            if m.get('type')!='http.request':raise ValueError('initialization body required')
            part=m.get('body',b'');total+=len(part)
            if total>65536:raise ValueError('bounded initialization required')
            parts.append(part)
            if not m.get('more_body'):break
        body=b''.join(parts);value=json.loads(body)
        if not isinstance(value,dict):raise ValueError('initialization object required')
        if value.get('jsonrpc')!='2.0' or value.get('method')!='initialize' or type(value.get('id')) not in (int,str):
            raise ValueError('native initialize required')
        from mcp.types import InitializeRequest
        InitializeRequest.model_validate(value)
        return body

    async def remove(self,actor):
        async def finish():
            actor.closing=True
            try:await actor.close()
            finally:
                if actor.sid:self.by_id.pop(actor.sid,None)
                self.actors.discard(actor)
        if actor.removal is None:actor.removal=asyncio.create_task(finish())
        await asyncio.shield(actor.removal)

    async def __call__(self,scope,receive,send):
        if scope.get('method')!='GET':
            if self.requests.locked():return await self.reply(send,503)
            async with self.requests:return await self.handle(scope,receive,send)
        return await self.handle(scope,receive,send)

    async def handle(self,scope,receive,send):
        ids=[v for k,v in scope.get('headers',[]) if k.lower()==b'mcp-session-id']
        if len(ids)>1:return await self.reply(send,400)
        sid=ids[0] if ids else None
        new=sid is None
        actor=None
        if new:
            if scope.get('method')!='POST':return await self.reply(send,400)
            if self.stopping or len(self.actors.items)>=self.actors.maximum:
                return await self.reply(send,503)
            # Reserve before reading any body or invoking the SDK.
            actor=Actor(self.create_module,self.tool_lock)
            self.actors.add(actor)
            try:body=await asyncio.wait_for(self.initialize_body(receive),timeout=10)
            except (ValueError,TypeError,KeyError,asyncio.TimeoutError):
                self.actors.discard(actor)
                return await self.reply(send,400)
            except BaseException:
                self.actors.discard(actor)
                raise
            actor.task=asyncio.create_task(actor.run())
            try:await asyncio.wait_for(actor.ready.wait(),timeout=10)
            except BaseException:
                await self.remove(actor)
                raise
            if actor.task.done():
                try:await actor.task
                except Exception:pass
                finally:self.actors.discard(actor)
                return await self.reply(send,500)
            original=receive;first=True
            async def replay():
                nonlocal first
                if first:first=False;return {'type':'http.request','body':body,'more_body':False}
                return await original()
            receive=replay
        else:
            actor=self.by_id.get(sid)
            if actor is None or actor.closing:return await self.reply(send,404)
            if scope.get('method')=='GET' and actor.streams:return await self.reply(send,409)
        actor.http+=1
        if scope.get('method')=='GET':actor.streams+=1
        actor.last=time.monotonic()
        status=None;complete=False
        async def capture(message):
            nonlocal status,complete
            if message['type']=='http.response.start':
                status=message['status']
                if new:
                    found=[v for k,v in message.get('headers',[]) if k.lower()==b'mcp-session-id']
                    if len(found)==1:
                        actor.sid=found[0];self.by_id[actor.sid]=actor
            if message['type']=='http.response.body' and not message.get('more_body'):
                complete=True
            await send(message)
        try:
            await actor.manager.handle_request(scope,receive,capture)
        finally:
            actor.http-=1
            if scope.get('method')=='GET':actor.streams-=1
            actor.last=time.monotonic()
            if scope.get('method')=='POST' and not complete:actor.uncertain=True
            if new and (not actor.sid or status!=200):actor.closing=True
            if scope.get('method')=='DELETE' and status in (200,204) and complete:actor.closing=True
            if actor.closing and not actor.http and not actor.callbacks:
                await self.remove(actor)

    async def reap_idle(self):
        while not self.stopping:
            await asyncio.sleep(30)
            for actor in list(self.actors.items):
                if actor.task is None or actor.http>actor.streams or actor.callbacks or actor.uncertain:continue
                if actor.closing or time.monotonic()-actor.last>=self.idle_seconds:
                    await self.remove(actor)


def session_owner_http(app,token):
    # Sixteen bounded actor streams plus two short requests. Streaming must
    # not occupy the request slots needed for calls and explicit DELETE.
    return OwnerHTTP(app,token,maximum_active=18)


def make_app(config,source,token):
    from starlette.applications import Starlette
    from starlette.routing import Route
    sys.path.insert(0,str(Path(config['module']).parent))
    router=SessionRouter(lambda:load_original(config,source))
    @asynccontextmanager
    async def lifespan(app):
        reaper=asyncio.create_task(router.reap_idle())
        try:yield
        finally:
            router.stopping=True;reaper.cancel()
            try:await reaper
            except asyncio.CancelledError:pass
            for actor in list(router.actors.items):await router.remove(actor)
    class Endpoint:
        async def __call__(self,scope,receive,send):await router(scope,receive,send)
    app=Starlette(routes=[Route('/mcp',Endpoint(),methods=['GET','POST','DELETE'])],lifespan=lifespan)
    app.state.session_router=router
    return session_owner_http(app,token)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config',required=True,type=Path)
    args=parser.parse_args();config,source,token=load_binding(args.config)
    fd=os.open(str(args.config)+'.lock',os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW,0o600)
    try:
        info=os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_uid!=os.geteuid() or stat.S_IMODE(info.st_mode)!=0o600:
            raise ValueError('unsafe singleton lock')
        fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        import uvicorn
        uvicorn.run(make_app(config,source,token),host='127.0.0.1',port=config['port'],workers=1,
                    access_log=False,log_level='error',limit_concurrency=22,
                    backlog=16,timeout_graceful_shutdown=8)
    finally:os.close(fd)


if __name__=='__main__':main()
