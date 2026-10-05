"""An A2A client that is not ours: the official SDK in Python (pip install a2a-sdk).

Usage: METAGENTE_TOKEN=... python client.py http://127.0.0.1:8080/agents/Weather/
Prints one JSON object with what the SDK saw. This is the client of `tests/interop/a2a_sdk_client.py`
of the original project, changed for this server: the card is read with the token, a message names
its skill in its metadata, and no task is kept, so GetTask finds none.
"""
import asyncio
import json
import os
import sys

import httpx
from a2a.client import A2ACardResolver, ClientConfig, create_client
from a2a.types.a2a_pb2 import GetTaskRequest, Message, Part, Role, SendMessageRequest, TaskState


def text_message(text, skill=None):
    message = Message(message_id="interop-1", role=Role.ROLE_USER, parts=[Part(text=text)])
    if skill:
        message.metadata.update({"skill": skill})
    return SendMessageRequest(message=message)


async def last_answer(client, request):
    answer = None
    async for response in client.send_message(request):
        answer = response
    return answer


async def main(agent_url):
    out = {}
    headers = {"Authorization": "Bearer " + os.environ["METAGENTE_TOKEN"]}
    async with httpx.AsyncClient(headers=headers) as http:
        card = await A2ACardResolver(http, agent_url).get_agent_card()
        out["card_name"] = card.name
        out["skills"] = [s.id for s in card.skills]
        out["interfaces"] = [(i.protocol_binding, i.protocol_version) for i in card.supported_interfaces]

        client = await create_client(card, client_config=ClientConfig(httpx_client=http, streaming=False))
        answer = await last_answer(client, text_message("Lisbon", skill="ask"))
        out["answered_with"] = answer.WhichOneof("payload") if hasattr(answer, "WhichOneof") else type(answer).__name__
        message = answer.message if answer.HasField("message") else None
        out["text"] = message.parts[0].text if message else None

        failed = await last_answer(client, text_message("x", skill="broken"))
        task = failed.task if failed.HasField("task") else None
        out["failed_state"] = TaskState.Name(task.status.state) if task else None
        out["failed_text"] = task.status.message.parts[0].text if task else None

        for name, call in [
            ("get_task_error", lambda: client.get_task(GetTaskRequest(id="nope"))),
            ("unknown_skill_error", lambda: last_answer(client, text_message("x", skill="dance"))),
        ]:
            try:
                await call()
                out[name] = None
            except Exception as e:  # the SDK raises its own error types
                out[name] = type(e).__name__ + ": " + str(e)

    async with httpx.AsyncClient() as anonymous:
        try:
            await A2ACardResolver(anonymous, agent_url).get_agent_card()
            out["without_token"] = "the card was given"
        except Exception as e:
            out["without_token"] = type(e).__name__ + ": " + str(e)[:120]
    print(json.dumps(out, indent=1))


asyncio.run(main(sys.argv[1]))
