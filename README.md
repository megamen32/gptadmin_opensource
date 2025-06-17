# GPTAdmin Services

This repository contains two small services used to remotely control a machine.

* **rootd.py** – runs as root and exposes low level operations.
* **hub_proxy.py** – collects heartbeats from multiple `rootd` servers and
  proxies requests to them.

## Requirements

```
pip install -r requirements.txt
```

## Running

Start `rootd` and `hub_proxy` in separate terminals:

```
ROOTD_TOKEN=srv_secret python rootd.py
CTL_TOKEN=chatgpt_secret python hub_proxy.py
```

`rootd` can register itself with the hub when `HUB_URL` is set. Each service
accepts tokens through environment variables as shown above.

## Tests

Basic scripts for manual testing are provided:

```
python hub_proxy.py & python rootd.py &
python test_rootd.py
python test_hub.py
```

## openapi.json

`openapi.json` documents the API served by `hub_proxy`. It was produced manualy, update it whenever the API changes to refresh the schema.

