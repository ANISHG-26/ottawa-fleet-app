# Validation and journey checks

Go unit tests live next to service packages. Fleet and ride database tests use
`TEST_DATABASE_URL` and skip with an explicit message if it is unset. Point it
only at a disposable local database. CI creates an ephemeral PostgreSQL 17.11
service and runs Go checks serially to avoid packages mutating shared tables at
the same time.

The operator's Playwright test uses contract fixtures without live services.
The Compose live journey is run separately against the real APIs and worker;
see [local development](../docs/local-development.md).

Run local checks with:

```powershell
python -m pip install -r scripts/requirements-contracts.txt
python scripts/dev.py check
```

The same workflow validates Go formatting/vet/tests, contracts, UI unit tests,
UI build, and the fixture browser smoke test.
