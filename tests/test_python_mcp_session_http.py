"""Fast local admission units (<1s/max5s). Fakes are NOT SDK/runtime proof."""
import asyncio
import importlib.util
from pathlib import Path
import sys
import unittest

sys.path.insert(0,str(Path(__file__).parents[1]/'tools'))
spec=importlib.util.spec_from_file_location('python_mcp_session_http',Path(__file__).parents[1]/'tools/python_mcp_session_http.py')
bridge=importlib.util.module_from_spec(spec);spec.loader.exec_module(bridge)

class Admission(unittest.TestCase):
    def test_sixteen_initialize_reservations_refuse_before_sdk_and_never_evict(self):
        pool=bridge.Actors();actors=[object() for _ in range(16)]
        for actor in actors:pool.add(actor)
        with self.assertRaisesRegex(ValueError,'capacity'):pool.add(object())
        self.assertEqual(pool.items,set(actors))

    def test_owned_completed_close_releases_only_its_slot(self):
        pool=bridge.Actors(maximum=2);a,b,c=object(),object(),object();pool.add(a);pool.add(b)
        pool.discard(a);pool.add(c);self.assertEqual(pool.items,{b,c})

    def test_invalid_limits_refuse(self):
        for n in [0,17,True,-1]:
            with self.assertRaises(ValueError):bridge.Actors(n)

class RouteLogic(unittest.IsolatedAsyncioTestCase):
    async def test_metadata_only_full_pool_refuses_before_body_or_sdk_allocation(self):
        router=bridge.SessionRouter(lambda:self.fail('module allocated'))
        for _ in range(16):router.actors.add(object())
        async def receive():self.fail('body consumed after capacity refusal')
        sent=[]
        async def send(m):sent.append(m)
        await router({'type':'http','method':'POST','headers':[]},receive,send)
        self.assertEqual(sent[0]['status'],503)

    async def test_cancelled_initial_body_releases_reservation_without_sdk(self):
        router=bridge.SessionRouter(lambda:self.fail('module allocated'));entered=asyncio.Event()
        async def body(receive):entered.set();await asyncio.Event().wait()
        router.initialize_body=body
        async def send(m):pass
        task=asyncio.create_task(router({'type':'http','method':'POST','headers':[]},None,send))
        await entered.wait();self.assertEqual(len(router.actors.items),1);task.cancel()
        with self.assertRaises(asyncio.CancelledError):await task
        self.assertFalse(router.actors.items)

    async def test_unknown_or_duplicate_session_does_not_create_module_or_actor(self):
        router=bridge.SessionRouter(lambda:self.fail('module allocated'))
        for headers,status in [([(b'mcp-session-id',b'foreign')],404),([(b'mcp-session-id',b'a'),(b'MCP-SESSION-ID',b'b')],400)]:
            sent=[]
            async def send(m):sent.append(m)
            await router({'type':'http','method':'POST','headers':headers},None,send)
            self.assertEqual(sent[0]['status'],status)
        self.assertFalse(router.actors.items)

    async def test_invalid_initialize_json_shapes_return400_without_allocation(self):
        for value in [b'[]',b'null',b'"text"']:
            router=bridge.SessionRouter(lambda:self.fail('module allocated'))
            async def receive():return {'type':'http.request','body':value,'more_body':False}
            sent=[]
            async def send(m):sent.append(m)
            await router({'type':'http','method':'POST','headers':[]},receive,send)
            self.assertEqual(sent[0]['status'],400)
            self.assertFalse(router.actors.items)

    async def test_two_sse_streams_leave_post_admission_and_duplicate_get_refuses(self):
        from types import SimpleNamespace
        router=bridge.SessionRouter(lambda:self.fail('module allocated'))
        held=asyncio.Event(); entered=[]
        async def handle(scope,receive,send):
            if scope['method']=='GET':entered.append(scope['headers'][0][1]);await held.wait()
            await send({'type':'http.response.start','status':200,'headers':[]})
            await send({'type':'http.response.body','body':b''})
        for sid in (b'a',b'b'):
            actor=bridge.Actor(lambda:None,router.tool_lock);actor.sid=sid
            actor.manager=SimpleNamespace(handle_request=handle)
            router.actors.add(actor);router.by_id[sid]=actor
        # Same authenticated outer admission used by the production app.
        app=bridge.session_owner_http(router,b'0'*64)
        async def send(m):pass
        def scope(method,sid):return {'type':'http','path':'/mcp','method':method,'headers':[(b'mcp-session-id',sid),(b'authorization',b'Bearer '+b'0'*64)]}
        tasks=[asyncio.create_task(app(scope('GET',sid),None,send)) for sid in (b'a',b'b')]
        try:
            for _ in range(10):
                if len(entered)==2:break
                await asyncio.sleep(0)
            sent=[]
            async def record(m):sent.append(m)
            await app(scope('POST',b'a'),None,record)
            self.assertEqual(sent[0]['status'],200)
            sent.clear();await app(scope('GET',b'a'),None,record)
            self.assertEqual(sent[0]['status'],409)
        finally:held.set();await asyncio.gather(*tasks)

    async def test_close_runs_once_on_owner_task_and_retains_neighbor(self):
        router=bridge.SessionRouter(lambda:None);a=bridge.Actor(lambda:None,router.tool_lock);b=bridge.Actor(lambda:None,router.tool_lock)
        router.actors.add(a);router.actors.add(b);a.sid=b'a';router.by_id[a.sid]=a;closed=[]
        async def close():closed.append(True);await asyncio.sleep(0)
        a.close=close
        await asyncio.gather(router.remove(a),router.remove(a))
        self.assertEqual(closed,[True]);self.assertNotIn(a.sid,router.by_id);self.assertEqual(router.actors.items,{b})

if __name__=='__main__':unittest.main()
