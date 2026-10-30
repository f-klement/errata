// Renovate's matchRegexOrGlob, from lib/util/string-match.ts and
// lib/util/regex.ts, with the minimatch and re2 versions Renovate pins
// (minimatch 10.2.6, re2 1.27.0). It reads renovate-cases.json, a list of
// [input, pattern, answer], on stdin, writes each case whose answer is not
// Renovate's, and exits 1 if there is one. The answer is true, false, or
// "throw" where Renovate throws, as it does when minimatch builds a regex
// JavaScript refuses. TestRenovateMatchAgreesWithRenovate holds errata to the
// same answers. To check them, or a case added, run in an empty directory:
//
//   npm install minimatch@10.2.6 re2@1.27.0
//   node <this file> < renovate-cases.json
//
// With --write it writes the cases instead, each with Renovate's answer.
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';

const require = createRequire(process.cwd() + '/');
const { minimatch } = require('minimatch');
const RE2 = require('re2');

const configValStart = /^!?\//;
const configValEnd = /\/i?$/;

function regexPredicate(input) {
  if (!(configValStart.test(input) && configValEnd.test(input))) return null;
  let re;
  try {
    const body = input.replace(configValStart, '').replace(configValEnd, '');
    re = input.endsWith('i') ? new RE2(body, 'i') : new RE2(body);
  } catch {
    return null;
  }
  const positive = !input.startsWith('!');
  return (x) => (positive ? re.test(x) : !re.test(x));
}

function matchRegexOrGlob(input, pattern) {
  if (pattern === '*') return true;
  const p = regexPredicate(pattern);
  if (p) return p(input);
  return minimatch(input, pattern, { dot: true, nocase: true });
}

function answer(s, p) {
  try {
    return matchRegexOrGlob(s, p);
  } catch {
    return 'throw';
  }
}

const cases = JSON.parse(readFileSync(0, 'utf8'));
if (process.argv.includes('--write')) {
  const rows = cases.map(([s, p]) => JSON.stringify([s, p, answer(s, p)]));
  console.log('[\n' + rows.join(',\n') + '\n]');
  process.exit(0);
}
let wrong = 0;
for (const [s, p, want] of cases) {
  const got = answer(s, p);
  if (got !== want) {
    wrong++;
    console.log(JSON.stringify([s, p, got]));
  }
}
console.log(`${cases.length} cases, ${wrong} not Renovate's answer`);
process.exit(wrong ? 1 : 0);
