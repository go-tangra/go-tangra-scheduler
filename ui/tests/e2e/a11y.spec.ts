import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { base, signIn } from './helpers'

// Every scheduler view inside the shell, both themes, zero serious or critical
// axe findings. Needs a full platform; skips without operator credentials.
const password = process.env.E2E_OPERATOR_PASSWORD ?? ''
const email = process.env.E2E_OPERATOR_EMAIL ?? 'ops@example.org'
const routes = ['/scheduler', '/scheduler/overview']
const tags = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']

test.describe('scheduler accessibility', () => {
  test.skip(!password, 'E2E_OPERATOR_PASSWORD not set')

  for (const theme of ['freya-light', 'freya-dark']) {
    test(`views are axe clean in ${theme}`, async ({ page }) => {
      await page.addInitScript((t) => localStorage.setItem('freya.theme', t), theme)
      await page.goto(base + '/')
      await signIn(page, email, password)
      for (const route of routes) {
        await page.goto(base + route)
        await expect(page.locator('main h1, main h2').first()).toBeVisible({ timeout: 15_000 })
        const results = await new AxeBuilder({ page }).withTags(tags).analyze()
        const blocking = results.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
        expect(blocking, route + ': ' + JSON.stringify(blocking.map((v) => ({ id: v.id, nodes: v.nodes.map((n) => n.target) })))).toEqual([])
        expect(await page.locator('[style]').count(), route + ': no inline styles').toBe(0)
      }
      // The task drawer (create form with the generated payload form).
      await page.goto(base + '/scheduler')
      const create = page.getByTestId('task-new')
      if (await create.isVisible()) {
        await create.click()
        await expect(page.getByTestId('task-drawer')).toBeVisible()
        const results = await new AxeBuilder({ page }).include('[data-test=task-drawer]').withTags(tags).analyze()
        const blocking = results.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
        expect(blocking, 'task drawer: ' + JSON.stringify(blocking.map((v) => ({ id: v.id, nodes: v.nodes.map((n) => n.target) })))).toEqual([])
      }
    })
  }
})
