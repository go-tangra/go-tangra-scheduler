import { afterEach, describe, expect, it } from 'vitest'
import { defineComponent, h, ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import SchemaForm from '@/components/SchemaForm.vue'
import { applyDefaults, cleanPayload, fromLocalInput, humanize, pointerOf, pointerToKey, schemaToForm, toLocalInput, validatePayload } from '@/utils/jsonSchema'
import type { JsonSchema } from '@/api/types'
import { mailType } from './helpers'

const schema = mailType.payload_schema as JsonSchema

describe('schemaToForm', () => {
  it('generates one field per property with kinds, required, enum, limits and descriptions', () => {
    const f = schemaToForm(schema)
    expect(f.supported).toBe(true)
    const by = Object.fromEntries(f.fields.map((x) => [x.key, x]))
    expect(by.recipient).toMatchObject({ kind: 'email', required: true, pointer: '/recipient', label: 'Recipient', description: 'Who gets the mail.' })
    expect(by.subject).toMatchObject({ kind: 'text', required: false, default: 'Hello' })
    expect(by.priority).toMatchObject({ kind: 'select', options: ['low', 'normal', 'high'] })
    expect(by.copies).toMatchObject({ kind: 'integer', minimum: 1, maximum: 5 })
    expect(by.urgent).toMatchObject({ kind: 'boolean' })
    expect(by.cc).toMatchObject({ kind: 'string-list' })
  })

  it('knows uuid, date-time, date, number, long text, titles, nullable types and exclusive bounds', () => {
    const f = schemaToForm({
      type: 'object',
      additionalProperties: false,
      properties: {
        id: { type: 'string', format: 'uuid' },
        at: { type: 'string', format: 'date-time' },
        day: { type: 'string', format: 'date' },
        ratio: { type: 'number', exclusiveMinimum: 0, maximum: 1 },
        n: { type: 'integer', exclusiveMaximum: 10 },
        body: { type: 'string', maxLength: 5000, title: 'Mail body' },
        note: { type: ['string', 'null'] },
        pick: { enum: ['a', 'b'] },
      },
    })
    expect(f.supported).toBe(true)
    expect(f.fields.map((x) => x.kind)).toEqual(['uuid', 'datetime', 'date', 'number', 'integer', 'textarea', 'text', 'select'])
    expect(f.fields[4]!.maximum).toBe(9)
    expect(f.fields[5]!.label).toBe('Mail body')
    expect(schemaToForm({ type: 'object', additionalProperties: false, properties: {} })).toEqual({ supported: true, fields: [] })
  })

  it.each([
    [null, 'any JSON object'],
    [{ type: 'object' }, 'any JSON object'],
    [{ type: 'array' }, 'not an object'],
    [{ oneOf: [] }, 'oneOf'],
    [{ type: 'object', properties: { a: { type: 'object', properties: {} } } }, '"a" has an unsupported type'],
    [{ type: 'object', properties: { a: { $ref: '#/x' } } }, '"a" uses $ref'],
    [{ type: 'object', properties: { a: { type: 'array', items: { type: 'integer' } } } }, 'other than plain text'],
    [{ type: 'object', properties: { a: { type: 'integer', enum: [1, 2] } } }, 'numeric choice list'],
    [{ type: 'object', properties: { a: { type: ['string', 'integer'] } } }, 'unsupported type'],
    [{ type: 'object', properties: { a: true } }, 'not a schema'],
    [{ type: 'object', properties: { a: { type: 'string' } }, additionalProperties: { type: 'string' } }, 'extra typed properties'],
  ])('falls back to JSON for %j', (s, reason) => {
    const f = schemaToForm(s as JsonSchema | null)
    expect(f.supported).toBe(false)
    expect(f.reason).toContain(reason)
  })

  it('pointers, labels, defaults and cleaning', () => {
    expect(pointerOf('a/b~c')).toBe('/a~1b~0c')
    expect(pointerToKey('/a~1b~0c/0')).toBe('a/b~c')
    expect(pointerToKey('#/tags/1')).toBe('tags')
    expect(pointerToKey('')).toBe('')
    expect(pointerToKey('/')).toBe('')
    expect(humanize('max_retries')).toBe('Max retries')
    expect(humanize('recipientEmail')).toBe('Recipient email')
    expect(applyDefaults(schema, { subject: 'Kept' })).toEqual({ subject: 'Kept', priority: 'normal' })
    expect(applyDefaults(null, { a: 1 })).toEqual({ a: 1 })
    const fields = schemaToForm(schema).fields
    expect(cleanPayload(fields, { recipient: 'a@b.cd', subject: '', copies: undefined, extra: 1 })).toEqual({ recipient: 'a@b.cd', extra: 1 })
  })

  it('validates required, email, uuid, enum, bounds, integers, lists, length and pattern', () => {
    const fields = schemaToForm({
      type: 'object',
      required: ['recipient', 'cc', 'urgent'],
      properties: {
        recipient: { type: 'string', format: 'email' },
        id: { type: 'string', format: 'uuid' },
        code: { type: 'string', minLength: 2, maxLength: 3, pattern: '^[A-Z]+$' },
        copies: { type: 'integer', minimum: 1, maximum: 5 },
        pick: { type: 'string', enum: ['a'] },
        cc: { type: 'array', items: { type: 'string' }, maxItems: 1 },
        urgent: { type: 'boolean' },
        at: { type: 'string', format: 'date-time' },
        day: { type: 'string', format: 'date' },
      },
    }).fields
    expect(validatePayload(fields, {})).toEqual({ '/recipient': 'This field is required.', '/cc': 'This field is required.' })
    const errs = validatePayload(fields, { recipient: 'nope', id: 'x', code: 'a', copies: 1.5, pick: 'b', cc: ['a', 'b'], at: 'soon', day: '1/2/2026' })
    expect(errs).toEqual({
      '/recipient': 'Enter a valid e-mail address.',
      '/id': 'Enter a valid identifier (UUID).',
      '/code': 'Must be at least 2 characters.',
      '/copies': 'Enter a whole number.',
      '/pick': 'Choose one of the listed values.',
      '/cc': 'Add at most 1.',
      '/at': 'Enter a valid date and time.',
      '/day': 'Enter a valid date.',
    })
    expect(validatePayload(fields, { recipient: 'a@b.cd', cc: ['x'], code: 'abc', copies: 9 })).toEqual({ '/code': 'This value has the wrong format.', '/copies': 'Must be at most 5.' })
    expect(validatePayload(fields, { recipient: 'a@b.cd', cc: ['x'], code: 'ABCD', copies: 0 })).toEqual({ '/code': 'Must be at most 3 characters.', '/copies': 'Must be at least 1.' })
    expect(validatePayload(fields, { recipient: 'a@b.cd', cc: ['x'], id: '018f3a2b-0000-7000-8000-000000000001', at: '2026-09-28T10:00:00Z', day: '2026-09-28' })).toEqual({})
  })

  it('converts between ISO and datetime-local', () => {
    const local = toLocalInput('2026-09-28T10:30:00Z')
    expect(local).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/)
    expect(fromLocalInput(local)).toBe('2026-09-28T10:30:00.000Z')
    expect(toLocalInput('bad')).toBe('')
    expect(toLocalInput(null)).toBe('')
    expect(fromLocalInput('')).toBe('')
    expect(fromLocalInput('bad')).toBe('')
  })
})

// A host that keeps the v-model like the task drawer does.
function host(s: JsonSchema | null, initial: Record<string, unknown> = {}, errors: Record<string, string> = {}) {
  const value = ref<Record<string, unknown>>(initial)
  const form = ref<InstanceType<typeof SchemaForm> | null>(null)
  const errs = ref(errors)
  const w = mount(defineComponent({
    setup: () => () => h(SchemaForm, { ref: form, schema: s, modelValue: value.value, errors: errs.value, 'onUpdate:modelValue': (v: Record<string, unknown>) => (value.value = v) }),
  }), { attachTo: document.body })
  return { w, value, form, errs }
}

describe('SchemaForm', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('renders the generated fields with required markers, help text and defaults', async () => {
    const { w, value, form } = host(schema, applyDefaults(schema))
    await flushPromises()
    const recipient = w.find<HTMLInputElement>('#payload-recipient')
    expect(recipient.attributes('type')).toBe('email')
    expect(w.find('[data-test="schema-field-recipient"]').text()).toContain('*')
    expect(w.text()).toContain('Who gets the mail.')
    expect(w.find<HTMLInputElement>('#payload-subject').element.value).toBe('Hello')
    expect(w.find<HTMLSelectElement>('#payload-priority').element.value).toBe('normal')
    expect(w.find('#payload-copies').attributes('type')).toBe('number')
    expect(w.find('#payload-copies').attributes('min')).toBe('1')
    expect(w.find('[data-test="schema-field-urgent"] input').exists()).toBe(true)

    await recipient.setValue('ada@example.org')
    await w.find('#payload-copies').setValue('3')
    await w.find('[data-test="schema-field-urgent"] input').trigger('click')
    const cc = w.find<HTMLInputElement>('#payload-cc')
    await cc.setValue('bob@example.org')
    await cc.trigger('keydown', { key: 'Enter' })
    await cc.setValue('eve@example.org')
    await cc.trigger('keydown', { key: ',' })
    expect(value.value).toEqual({ subject: 'Hello', priority: 'normal', recipient: 'ada@example.org', copies: 3, urgent: true, cc: ['bob@example.org', 'eve@example.org'] })
    expect(w.findAll('[data-test^="payload-cc-chip-"]').length).toBe(2)
    await w.find('[data-test="payload-cc-chip-0"] button').trigger('click')
    expect(value.value.cc).toEqual(['eve@example.org'])
    await cc.trigger('keydown', { key: 'Backspace' })
    expect(value.value.cc).toBeUndefined()
    expect(form.value!.validate()).toBe(true)
    w.unmount()
  })

  it('validate() marks missing required and bad formats under the field', async () => {
    const { w, form } = host(schema, { recipient: 'nope' })
    await flushPromises()
    expect(form.value!.validate()).toBe(false)
    await flushPromises()
    expect(w.find('[data-test="schema-field-recipient"]').text()).toContain('Enter a valid e-mail address.')
    await w.find('#payload-recipient').setValue('ok@example.org')
    expect(w.find('[data-test="schema-field-recipient"]').text()).not.toContain('Enter a valid e-mail address.')
    w.unmount()
  })

  it('maps server errors by JSON pointer onto their field; unknown pointers go to the banner', async () => {
    const { w } = host(schema, { recipient: 'a@b.cd' }, { '/recipient': 'mailbox does not exist', '/cc/1': 'bad address', '/other': 'unexpected', '': 'too big' })
    await flushPromises()
    expect(w.find('[data-test="schema-field-recipient"]').text()).toContain('mailbox does not exist')
    expect(w.find('[data-test="schema-field-cc"]').text()).toContain('bad address')
    const banner = w.find('[data-test="schema-form-errors"]').text()
    expect(banner).toContain('/other: unexpected')
    expect(banner).toContain('too big')
    expect(banner).not.toContain('mailbox')
    w.unmount()
  })

  it('switches to a raw JSON editor and back; invalid JSON blocks the switch and validate()', async () => {
    const { w, value, form } = host(schema, { recipient: 'a@b.cd' })
    await flushPromises()
    await w.find('[data-test="schema-form-json-toggle"]').trigger('click')
    const ta = w.find<HTMLTextAreaElement>('textarea')
    expect(JSON.parse(ta.element.value)).toEqual({ recipient: 'a@b.cd' })
    await ta.setValue('{"recipient": "x@y.zz", "extra": [1]}')
    expect(value.value).toEqual({ recipient: 'x@y.zz', extra: [1] })
    await ta.setValue('{"recipient": ')
    expect(w.text()).toContain('Not valid JSON')
    expect(form.value!.validate()).toBe(false)
    expect(w.find('[data-test="schema-form-json-toggle"]').attributes('disabled')).toBeDefined()
    await ta.setValue('[1]')
    expect(w.text()).toContain('must be a JSON object')
    await ta.setValue('')
    expect(value.value).toEqual({})
    await ta.setValue('{"recipient": "b@c.dd"}')
    expect(form.value!.validate()).toBe(true)
    await w.find('[data-test="schema-form-json-toggle"]').trigger('click')
    expect(w.find<HTMLInputElement>('#payload-recipient').element.value).toBe('b@c.dd')
    w.unmount()
  })

  it('always uses the JSON editor for an unsupported or null schema', async () => {
    const { w, value, form } = host(null, { anything: true })
    await flushPromises()
    expect(w.find('[data-test="schema-form-fallback"]').text()).toContain('any JSON object')
    expect(w.find('[data-test="schema-form-json-toggle"]').exists()).toBe(false)
    const ta = w.find<HTMLTextAreaElement>('textarea')
    expect(JSON.parse(ta.element.value)).toEqual({ anything: true })
    await ta.setValue('{"a": {"nested": 1}}')
    expect(value.value).toEqual({ a: { nested: 1 } })
    expect(form.value!.validate()).toBe(true)
    w.unmount()

    const nested = host({ type: 'object', properties: { a: { type: 'object' } } })
    await flushPromises()
    expect(nested.w.find('[data-test="schema-form-fallback"]').text()).toContain('"a" has an unsupported type')
    nested.w.unmount()
  })

  it('a schema without properties says there is no payload', async () => {
    const { w } = host({ type: 'object', additionalProperties: false })
    await flushPromises()
    expect(w.find('[data-test="schema-form-empty"]').exists()).toBe(true)
    w.unmount()
  })

  it('date-time fields edit local time and store ISO', async () => {
    const { w, value } = host({ type: 'object', properties: { at: { type: 'string', format: 'date-time' } } }, { at: '2026-09-28T10:30:00Z' })
    await flushPromises()
    const input = w.find<HTMLInputElement>('#payload-at')
    expect(input.attributes('type')).toBe('datetime-local')
    expect(input.element.value).toBe(toLocalInput('2026-09-28T10:30:00Z'))
    await input.setValue(toLocalInput('2026-10-01T08:00:00Z'))
    expect(value.value.at).toBe('2026-10-01T08:00:00.000Z')
    await input.setValue('')
    expect(value.value.at).toBeUndefined()
    w.unmount()
  })
})
