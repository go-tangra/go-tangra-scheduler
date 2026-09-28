import { describe, expect, it } from 'vitest'
import { routes } from '@/remote/routes'
import { nav } from '@/remote/nav'
// Read as text: the config is Node-side (process.env) and outside the app program.
import federationConfig from '../../module-federation.config.ts?raw'

describe('scheduler remote', () => {
  it('exports the module routes, all tagged with the scheduler module', () => {
    expect(routes.map((r) => r.path)).toEqual(['/scheduler', '/scheduler/dashboard', '/scheduler/overview'])
    for (const r of routes) expect(r.meta?.module).toBe('scheduler')
    expect(nav()).toEqual([])
  })
  it('the former overview path redirects to the dashboard', () => {
    expect(routes.find((r) => r.path === '/scheduler/overview')?.redirect).toBe('/scheduler/dashboard')
  })
  it('every route lazily resolves a component', async () => {
    for (const r of routes.filter((x) => !x.redirect)) {
      const load = r.component as () => Promise<{ default: unknown }>
      expect((await load()).default).toBeTruthy()
    }
  })
  it('the federation remote is "scheduler" exposing ./routes and ./nav with host-only kit singletons', () => {
    expect(federationConfig).toContain("name: 'scheduler'")
    expect(federationConfig).toContain("'./routes': './src/remote/routes.ts'")
    expect(federationConfig).toContain("'./nav': './src/remote/nav.ts'")
    expect(federationConfig).toMatch(/'@go-tangra\/ui': \{ singleton: true, requiredVersion: '\^4\.0\.0', strictVersion: true, \.\.\.hostOnly \}/)
  })
})
