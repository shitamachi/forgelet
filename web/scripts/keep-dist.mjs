// A clean checkout still needs an embeddable directory for Go-only tests.
import { writeFileSync } from 'node:fs'
writeFileSync(new URL('../../internal/webui/dist/.keep', import.meta.url), '')
