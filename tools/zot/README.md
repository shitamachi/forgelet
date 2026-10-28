# Scoped application publication in the existing Zot

The Tasks image workflow publishes to
`zot.k.mygo.rocks/forgelet/apps/steam-task` using repository secrets
`ZOT_USERNAME` and `ZOT_PASSWORD`. Its dedicated `forgelet-steam-tasks-push`
account can read, create and update that exact image repository, but cannot
delete images or write to other repositories. Public upstream mirrors remain
read-only and application images remain private. The workflow keeps its
reliability dependency, migration checks, tests, timeout and tag rules.

`tasks-publisher-acl.json` records the deployed access-control policy, without
passwords or hashes. Merge its policy with the registry's existing configuration;
it is not a complete Zot installation configuration. Existing user permissions
must be preserved explicitly. In Zot v2.1.22 an empty user-specific action list
does not deny access granted by a broad default policy. Therefore the catch-all
default is empty, existing users receive explicit permissions, and the publisher
is allowed only at its exact application path. Never give workflows the registry
administrator password.

Operational credentials are generated locally, stored with private permissions,
added as a bcrypt entry alongside existing htpasswd users, and saved in Forgelet's
encrypted repository secrets and GitHub repository secrets. Native pulls can
continue using an independently managed read credential. The running Tasks
production deployment and historical rollback digests are not changed by a
publication-target migration; a later deployment must use a verified Zot digest
and an appropriate namespace pull secret.

The regression starts real Zot v2.1.22 with the candidate ACL and disposable test
passwords. It verifies create/read/update, exact `latest` manifest digest,
unchanged admin access, public read and denied cross-repository writes, deletion
and anonymous application reads. No production credential is read by the test.

```sh
FORGELET_TEST_ZOT_IMAGE=your-cached-zot-v2.1.22-image \
FORGELET_TEST_ZOT_CONFIG="$PWD/tools/zot/tasks-publisher-acl.json" \
  go test -tags=integration ./test/integration \
  -run '^TestZotScopedPublisher$' -count=1 -v -timeout=4m
```

The fixture isolates storage, listeners and extensions. The initial config is
otherwise preserved for authorization. Real-cluster ACL checks and a real GitHub
push separately verify the deployed registry and original Actions execution.
No new registry service, persistent worker or PVC is introduced.

Upstream: [Zot authentication and authorization](https://zotregistry.dev/v2.1.22/articles/authn-authz/).
