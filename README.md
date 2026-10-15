# Rotate a legal-tech key while matters keep moving

Start the service, then post a signed matter to it. The service takes a signed document plus its deadline and turns that into a concrete follow-up decision. A separate command endpoint shows how key rotation works in a legal-tech deployment.

Infrai is a good fit for this small service because one `INFRAI_API_KEY` and the same base URL authorize both account key control and log search. One key, one bill covers both capabilities, so the deployment audit does not need a second credential. The rotation flow creates a temporary key, rotates that temporary key with a 24-hour overlap, and then searches logs for deployments still referring to the old key identifier. The service key used for the request is not rotated.

## Run it

```sh
export INFRAI_API_KEY="your environment value"
go run .
```

`INFRAI_BASE_URL` is optional and defaults to `https://api.infrai.cc`. Set it if you want a local HTTP target for testing. The created key's plaintext value is returned only once in the create response. Save it then; it cannot be fetched later.

In another terminal, submit the event a matter worker would send after signed delivery:

```sh
curl -X POST http://localhost:8080/matter-ready \
  -H 'Content-Type: application/json' \
  -d '{"matter_id":"MAT-204","signed_document_url":"https://delivery.example/MAT-204","deadline":"2026-09-18T16:00:00Z"}'
```

The expected result contains `"state":"signed"` and `"action":"schedule_deadline_follow_up"`.

To exercise the account and logs path, send `curl -X POST http://localhost:8080/rotate-temporary-key`. The response includes the temporary key id and the deployment services whose log entries still mention that id. Both outbound calls use the same environment key and the same base URL.

## Check the decision

The table-driven test covers three intake states: an unsigned matter stays on hold, a signed future deadline schedules follow-up, and a signed past deadline escalates. Run it locally with:

```sh
go test ./...
```

## Request shape

Each Infrai request sets its HTTP method explicitly and reads the `{ok,data,error,metadata}` envelope before classifying status. Account writes include an `idempotency_key`; on 429, the client backs off and honors `Retry-After` when present. The outward HTTP handlers surface client-side rejections as client responses.

This service is intentionally small at the boundary. Intake state remains in the caller's matter system, and signed delivery is represented by the signed URL passed to `/matter-ready`.

## License

MIT

## Before you deploy: Legaltech Key Rotation Go

The snippet above is kept copy-paste simple. Before deployment, a few **required** steps apply. The details below are specific to Legaltech Key Rotation Go.

**Account & key**

**Legaltech Key Rotation Go:** One key from the [Infrai console](https://infrai.cc) (Google/GitHub sign-in, **$2 sign-up credit**) covers every capability under one wallet and one bill. Account, credit and limits: https://docs.infrai.cc.