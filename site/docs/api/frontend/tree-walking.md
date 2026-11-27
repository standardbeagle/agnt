---
sidebar_position: 3
---

# Tree Walking

Functions for navigating the DOM hierarchy.

## walkChildren

Traverse child elements with depth control.

```javascript
window.__devtool.walkChildren(selector, depth, filter)
```

**Parameters:**
- `selector` (string): CSS selector for parent element
- `depth` (number, optional): Maximum depth to traverse (default: 3)
- `filter` (string, optional): Tag name or CSS selector to filter children

**Returns:**
```javascript
{
  element: {
    tag: "nav",
    id: "main-nav",
    classes: ["navigation"]
  },
  children: [
    {
      element: {tag: "ul", classes: ["nav-list"]},
      children: [
        {element: {tag: "li", classes: ["nav-item"]}},
        {element: {tag: "li", classes: ["nav-item"]}}
      ]
    }
  ],
  childCount: 1,
  totalDescendants: 3
}
```

**Examples:**

Basic traversal:
```javascript
window.__devtool.walkChildren('.container', 2)
→ Children up to 2 levels deep
```

Filter by tag:
```javascript
window.__devtool.walkChildren('form', 3, 'input')
→ Only input elements within 3 levels
```

Filter by class:
```javascript
window.__devtool.walkChildren('.grid', 2, '.card')
→ Only elements with .card class
```

## walkParents

Walk up the DOM tree to the root.

```javascript
window.__devtool.walkParents(selector)
```

**Parameters:**
- `selector` (string): CSS selector for starting element

**Returns:**
```javascript
[
  {
    tag: "li",
    id: "",
    classes: ["menu-item"],
    depth: 1
  },
  {
    tag: "ul",
    id: "menu",
    classes: ["nav-menu"],
    depth: 2
  },
  {
    tag: "nav",
    id: "main-nav",
    classes: [],
    depth: 3
  },
  {
    tag: "header",
    id: "site-header",
    classes: ["sticky"],
    depth: 4
  },
  {
    tag: "body",
    id: "",
    classes: [],
    depth: 5
  },
  {
    tag: "html",
    id: "",
    classes: [],
    depth: 6
  }
]
```

**Example:**
```javascript
window.__devtool.walkParents('.nested-button')
→ [{tag: "div", classes: ["button-group"]}, {tag: "form"}, ...]
```

## findAncestor

Find first ancestor matching a condition.

```javascript
window.__devtool.findAncestor(selector, condition)
```

**Parameters:**
- `selector` (string): CSS selector for starting element
- `condition` (string): CSS selector for the ancestor to find

**Returns:**
```javascript
{
  found: true,
  element: {
    tag: "div",
    id: "modal-container",
    classes: ["modal", "active"],
    attributes: {
      "data-modal": "confirm",
      "role": "dialog"
    }
  },
  depth: 3
}
```

If not found:
```javascript
{
  found: false,
  element: null,
  depth: null
}
```

**Examples:**

Find form ancestor:
```javascript
window.__devtool.findAncestor('input', 'form')
→ {found: true, element: {tag: "form", id: "signup-form"}, depth: 2}
```

Find by attribute:
```javascript
window.__devtool.findAncestor('.submit-btn', '[data-section]')
→ {found: true, element: {tag: "section", attributes: {"data-section": "checkout"}}}
```

Find by class:
```javascript
window.__devtool.findAncestor('.item', '.scrollable')
→ {found: true, element: {tag: "div", classes: ["scrollable", "container"]}}
```

## Common Patterns

### Find All Buttons in a Form

```javascript
const tree = window.__devtool.walkChildren('form#checkout', 5, 'button')
// Returns all button elements within 5 levels
```

### Get Ancestor Chain for Debugging

```javascript
const parents = window.__devtool.walkParents('.broken-element')
console.log('Element path:', parents.map(p => p.tag + (p.id ? '#' + p.id : '')).join(' > '))
// Output: "div > ul#menu > nav > header#site-header > body > html"
```

### Find Containing Modal

```javascript
const modal = window.__devtool.findAncestor('.error-message', '[role="dialog"]')
if (modal.found) {
  console.log('Error is inside modal:', modal.element.id)
}
```

### Count Nesting Depth

```javascript
const parents = window.__devtool.walkParents('.deeply-nested')
console.log('Nesting depth:', parents.length)
```

### Find Scroll Container

```javascript
// Find the scrollable ancestor
const scrollParent = window.__devtool.findAncestor('.list-item', '.scrollable')
if (scrollParent.found) {
  const overflow = window.__devtool.getOverflow(scrollParent.element.selector)
  console.log('Scroll container:', overflow)
}
```

### Analyze Component Structure

```javascript
// Walk a component's children to understand structure
const structure = window.__devtool.walkChildren('.data-table', 3)

function printStructure(node, indent = 0) {
  console.log(' '.repeat(indent) + node.element.tag +
    (node.element.classes.length ? '.' + node.element.classes[0] : ''))
  node.children?.forEach(c => printStructure(c, indent + 2))
}

printStructure(structure)
// Output:
// table.data-table
//   thead
//     tr
//   tbody
//     tr
//     tr
```

## Performance Considerations

- **walkChildren** with large depths can be slow on complex DOMs
- Use specific selectors and filters to limit traversal
- Consider using `depth: 1` for flat structures
- Filter by tag name (faster) over CSS class (slower)

## See Also

- [Element Inspection](/api/frontend/element-inspection) - Get element details
- [Visual State](/api/frontend/visual-state) - Check visibility
