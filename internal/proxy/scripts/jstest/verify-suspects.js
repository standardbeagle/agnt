import { auditPage, opportunities } from './load-audit.js';

// Test the SUSPECT cases to confirm they stay silent
const suspectCases = [
  { name: 'min-width: fit-content', css: '.box { min-width: fit-content; }' },
  { name: 'max-width: max-content', css: '.box { max-width: max-content; }' },
  { name: 'min-height: fit-content', css: '.box { min-height: fit-content; }' },
  { name: 'max-height: max-content', css: '.box { max-height: max-content; }' },
  { name: 'grid-template-columns: max-content', css: '.grid { grid-template-columns: max-content 1fr; }' },
  { name: 'flex-basis: fit-content', css: '.flex { flex-basis: fit-content; }' },
];

console.log('SUSPECT CASE VERIFICATION:');
console.log('==========================\n');

for (const testCase of suspectCases) {
  const result = auditPage(testCase.css);
  const found = opportunities(result, 'shrink-to-fit');
  const stored = document.styleSheets[0].cssRules[0].style.cssText;
  
  console.log(`${testCase.name}:`);
  console.log(`  CSS stored: ${stored}`);
  console.log(`  Findings: ${found.length} (expected: 0, actual: ${found.length === 0 ? 'SILENT ✓' : 'FIRED ✗'})`);
  console.log();
}
