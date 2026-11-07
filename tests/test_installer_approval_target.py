"""Pure identity selection; real installer lifecycle covers HTTP and processes."""
import pytest
import cli

IDENTITY = {'server_id': 'signed-local', 'public_key': 'key-local', 'fingerprint': 'fp-local'}

def agent(route='shell:renamed-host', **changes):
    return {'agent_id': route, 'kind': 'virtual_shell', 'status': 'awaiting_approval',
            'meta': dict(IDENTITY), **changes}


def test_short_discovery_target_resolves_to_identity_matched_canonical_agent():
    candidate = {'server_id': 'h0123456789ab', 'status': 'awaiting_approval', 'meta': IDENTITY}
    foreign = agent('shell:local-hostname', meta={**IDENTITY, 'public_key': 'foreign-key'})
    assert cli._local_shell_approval_target(candidate, IDENTITY, [foreign, agent()]) == 'shell:renamed-host'


@pytest.mark.parametrize('agents', [[], [agent(), agent('shell:duplicate')],
    [agent(meta={**IDENTITY, 'public_key': 'foreign-key'})]])
def test_short_discovery_target_never_guesses_identity(agents):
    with pytest.raises(RuntimeError):
        cli._local_shell_approval_target({'server_id': 'h0123456789ab'}, IDENTITY, agents)


def test_legacy_canonical_discovery_still_works():
    assert cli._local_shell_approval_target({'server_id': 'shell:legacy'}, IDENTITY, []) == 'shell:legacy'
