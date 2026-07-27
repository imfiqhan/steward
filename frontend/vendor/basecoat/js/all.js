(() => {
  const componentRegistry = {};
  let observer = null;

  const registerComponent = (name, selectorOrOptions, initFunction) => {
    const options = typeof selectorOrOptions === 'object'
      ? selectorOrOptions
      : { selector: selectorOrOptions, init: initFunction };

    componentRegistry[name] = {
      selector: options.selector,
      init: options.init,
      refresh: options.refresh,
    };
  };

  const initComponent = (element, componentName) => {
    const component = componentRegistry[componentName];
    if (!component) return;

    try {
      component.init(element);
      if (element.hasAttribute(`data-${componentName}-initialized`)) {
        element.dataset.basecoatComponent = componentName;
      }
    } catch (error) {
      console.error(`Failed to initialize ${componentName}:`, error);
      if (typeof element._destroy === 'function') {
        try {
          element._destroy();
        } catch (destroyError) {
          console.error(`Failed to clean up ${componentName} after initialization error:`, destroyError);
        }
      }
      delete element._destroy;
      element.removeAttribute(`data-${componentName}-initialized`);
      delete element.dataset.basecoatComponent;
    }
  };

  const destroyComponent = (element) => {
    if (!element || element.nodeType !== Node.ELEMENT_NODE) return;
    const componentName = element.dataset?.basecoatComponent;

    if (typeof element._destroy === 'function') {
      try {
        element._destroy();
      } catch (error) {
        console.error('Failed to destroy Basecoat component:', error);
      }
    }

    delete element._destroy;
    if (componentName) element.removeAttribute(`data-${componentName}-initialized`);
    delete element.dataset.basecoatComponent;
  };

  const destroyRemovedComponents = (node) => {
    if (node.nodeType !== Node.ELEMENT_NODE) return;
    if (node.isConnected) return;

    if (node.dataset?.basecoatComponent) destroyComponent(node);
    node.querySelectorAll('[data-basecoat-component]').forEach(destroyComponent);
  };

  const uniqueElements = (elements) => Array.from(new Set(elements));

  const getComponentElements = (componentName, selector, force = false) => {
    const elements = Array.from(document.querySelectorAll(selector));
    if (force) {
      elements.push(...document.querySelectorAll(`[data-basecoat-component="${componentName}"]`));
    }
    return uniqueElements(elements);
  };

  const initAllComponents = (options = {}) => {
    const force = options.force === true;
    Object.entries(componentRegistry).forEach(([name, { selector }]) => {
      getComponentElements(name, selector, force).forEach((element) => {
        const wasComponent = element.dataset?.basecoatComponent === name;
        if (force) destroyComponent(element);
        if (wasComponent || element.matches(selector)) initComponent(element, name);
      });
    });
  };

  const initNewComponents = (node) => {
    if (node.nodeType !== Node.ELEMENT_NODE) return;

    Object.entries(componentRegistry).forEach(([name, { selector }]) => {
      if (node.matches(selector)) initComponent(node, name);
      node.querySelectorAll(selector).forEach(element => initComponent(element, name));
    });
  };

  const refreshComponent = (element) => {
    if (!element) return;
    if (typeof element.refresh === 'function') {
      element.refresh();
      return;
    }

    const componentName = element.dataset?.basecoatComponent;
    const component = componentName ? componentRegistry[componentName] : null;
    if (component?.refresh) {
      component.refresh(element);
    }
  };

  const startObserver = () => {
    if (observer) return;

    observer = new MutationObserver((mutations) => {
      mutations.forEach((mutation) => {
        mutation.addedNodes.forEach(initNewComponents);
        mutation.removedNodes.forEach(destroyRemovedComponents);
      });
    });

    observer.observe(document.body, { childList: true, subtree: true });
  };

  const stopObserver = () => {
    if (!observer) return;
    observer.disconnect();
    observer = null;
  };

  const initRegisteredComponent = (componentName, options = {}) => {
    const component = componentRegistry[componentName];
    if (!component) {
      console.warn(`Component '${componentName}' not found in registry`);
      return;
    }

    const force = options.force === true;
    getComponentElements(componentName, component.selector, force).forEach((element) => {
      const wasComponent = element.dataset?.basecoatComponent === componentName;
      if (force) destroyComponent(element);
      if (wasComponent || element.matches(component.selector)) initComponent(element, componentName);
    });
  };

  const initAllRegisteredComponents = (options = {}) => {
    initAllComponents(options);
  };

  const setTheme = (mode) => {
    const dark = mode === 'dark';
    document.documentElement.classList.toggle('dark', dark);
    try { localStorage.setItem('themeMode', dark ? 'dark' : 'light'); } catch (_) {}
    document.dispatchEvent(new CustomEvent('basecoat:themechange', { detail: { mode: dark ? 'dark' : 'light' } }));
  };

  const getTheme = () => document.documentElement.classList.contains('dark') ? 'dark' : 'light';

  window.basecoat = {
    register: registerComponent,
    init: initRegisteredComponent,
    initAll: initAllRegisteredComponents,
    refresh: refreshComponent,
    start: startObserver,
    stop: stopObserver,
    theme: {
      get: getTheme,
      set: setTheme,
      toggle: () => setTheme(getTheme() === 'dark' ? 'light' : 'dark'),
    },
  };

  document.addEventListener('DOMContentLoaded', () => {
    initAllComponents();
    startObserver();
  });
})();

(() => {
  const states = new WeakMap();

  const isDisabled = (details) => {
    const summary = details.querySelector(':scope > summary');
    return details.getAttribute('aria-disabled') === 'true'
      || details.dataset.disabled === 'true'
      || summary?.getAttribute('aria-disabled') === 'true';
  };

  const isMultiple = (root) => root.hasAttribute('data-multiple');

  const closeSiblings = (root, activeDetails) => {
    if (isMultiple(root) || !activeDetails.open) return;
    root.querySelectorAll(':scope > details[open]').forEach((details) => {
      if (details !== activeDetails) details.open = false;
    });
  };

  const initAccordion = (root) => {
    if (root.dataset.accordionInitialized) return;

    const handleClick = (event) => {
      const summary = event.target.closest('summary');
      const details = summary?.closest('details');
      if (!details || details.parentElement !== root || !isDisabled(details)) return;
      event.preventDefault();
    };

    const handleKeydown = (event) => {
      if (event.key !== 'Enter' && event.key !== ' ') return;
      const summary = event.target.closest('summary');
      const details = summary?.closest('details');
      if (!details || details.parentElement !== root || !isDisabled(details)) return;
      event.preventDefault();
    };

    const handleToggle = (event) => {
      const details = event.target;
      if (details.parentElement !== root) return;
      if (isDisabled(details)) {
        details.open = false;
        return;
      }
      closeSiblings(root, details);
    };

    root.addEventListener('click', handleClick);
    root.addEventListener('keydown', handleKeydown);
    root.addEventListener('toggle', handleToggle, true);
    root.querySelectorAll(':scope > details[open]').forEach((details) => closeSiblings(root, details));

    states.set(root, { handleClick, handleToggle });
    root._destroy = () => {
      root.removeEventListener('click', handleClick);
      root.removeEventListener('keydown', handleKeydown);
      root.removeEventListener('toggle', handleToggle, true);
      states.delete(root);
    };

    root.dataset.accordionInitialized = 'true';
    root.dispatchEvent(new CustomEvent('basecoat:initialized'));
  };

  if (window.basecoat) {
    window.basecoat.register('accordion', '.accordion:not([data-accordion-initialized])', initAccordion);
  }
})();

(() => {
  const states = new WeakMap();

  const isDisabled = (item) =>
    item.hasAttribute('disabled') ||
    item.getAttribute('aria-disabled') === 'true' ||
    item.getAttribute('data-disabled') === 'true';

  const getElements = (container) => ({
    input: container.querySelector('header input'),
    menu: container.querySelector('[role="menu"]'),
  });

  const getItems = (menu) => {
    const allItems = Array.from(menu.querySelectorAll('[role="menuitem"]'));
    return {
      allItems,
      items: allItems.filter(item => !isDisabled(item)),
    };
  };

  const scrollItemIntoMenu = (state, item) => {
    const itemRect = item.getBoundingClientRect();
    const menuRect = state.menu.getBoundingClientRect();

    if (itemRect.top < menuRect.top) {
      state.menu.scrollTop -= menuRect.top - itemRect.top;
    } else if (itemRect.bottom > menuRect.bottom) {
      state.menu.scrollTop += itemRect.bottom - menuRect.bottom;
    }
  };

  const setActiveItem = (state, index) => {
    if (state.activeIndex > -1 && state.items[state.activeIndex]) {
      state.items[state.activeIndex].classList.remove('active');
    }

    state.activeIndex = index;

    if (state.activeIndex > -1) {
      const activeItem = state.items[state.activeIndex];
      activeItem.classList.add('active');
      if (activeItem.id) {
        state.input.setAttribute('aria-activedescendant', activeItem.id);
      } else {
        state.input.removeAttribute('aria-activedescendant');
      }
    } else {
      state.input.removeAttribute('aria-activedescendant');
    }
  };

  const filterItems = (state) => {
    if (state.manualFilter) {
      setActiveItem(state, -1);
      state.visibleItems = state.items.filter(item => item.getAttribute('aria-hidden') !== 'true');
      if (state.visibleItems.length > 0) {
        setActiveItem(state, state.items.indexOf(state.visibleItems[0]));
      }
      return;
    }

    const searchTerm = state.input.value.trim().toLowerCase();

    setActiveItem(state, -1);
    state.visibleItems = [];

    state.allItems.forEach(item => {
      if (item.hasAttribute('data-force')) {
        item.setAttribute('aria-hidden', 'false');
        if (state.items.includes(item)) state.visibleItems.push(item);
        return;
      }

      const itemText = (item.dataset.filter || item.textContent).trim().toLowerCase();
      const keywordList = (item.dataset.keywords || '')
        .toLowerCase()
        .split(/[\s,]+/)
        .filter(Boolean);
      const matchesKeyword = keywordList.some(keyword => keyword.includes(searchTerm));
      const matches = itemText.includes(searchTerm) || matchesKeyword;
      item.setAttribute('aria-hidden', String(!matches));
      if (matches && state.items.includes(item)) state.visibleItems.push(item);
    });

    if (state.visibleItems.length > 0) {
      setActiveItem(state, state.items.indexOf(state.visibleItems[0]));
      scrollItemIntoMenu(state, state.visibleItems[0]);
    }
  };

  const refreshCommand = (container) => {
    const state = states.get(container);
    if (!state) return;

    const elements = getElements(container);
    if (!elements.input || !elements.menu) {
      const missing = [];
      if (!elements.input) missing.push('input');
      if (!elements.menu) missing.push('menu');
      console.error(`Command component refresh failed. Missing element(s): ${missing.join(', ')}`, container);
      return;
    }

    Object.assign(state, elements, getItems(elements.menu));
    state.manualFilter = container.dataset.filter === 'manual';
    filterItems(state);
  };

  const handleKeyNavigation = (event, state) => {
    if (!['ArrowDown', 'ArrowUp', 'Enter', 'Home', 'End'].includes(event.key)) return;

    if (event.key === 'Enter') {
      event.preventDefault();
      if (state.activeIndex > -1) state.items[state.activeIndex]?.click();
      return;
    }

    if (state.visibleItems.length === 0) return;

    event.preventDefault();

    const currentVisibleIndex = state.activeIndex > -1 ? state.visibleItems.indexOf(state.items[state.activeIndex]) : -1;
    let nextVisibleIndex = currentVisibleIndex;

    if (event.key === 'ArrowDown' && currentVisibleIndex < state.visibleItems.length - 1) nextVisibleIndex = currentVisibleIndex + 1;
    if (event.key === 'ArrowUp') nextVisibleIndex = currentVisibleIndex > 0 ? currentVisibleIndex - 1 : 0;
    if (event.key === 'Home') nextVisibleIndex = 0;
    if (event.key === 'End') nextVisibleIndex = state.visibleItems.length - 1;

    if (nextVisibleIndex !== currentVisibleIndex) {
      const newActiveItem = state.visibleItems[nextVisibleIndex];
      setActiveItem(state, state.items.indexOf(newActiveItem));
      scrollItemIntoMenu(state, newActiveItem);
    }
  };

  const initCommand = (container) => {
    if (container.dataset.commandInitialized) return;

    const state = { activeIndex: -1, allItems: [], items: [], visibleItems: [], manualFilter: false };
    states.set(container, state);

    container.refresh = () => refreshCommand(container);

    const elements = getElements(container);
    if (!elements.input || !elements.menu) {
      const missing = [];
      if (!elements.input) missing.push('input');
      if (!elements.menu) missing.push('menu');
      console.error(`Command component initialization failed. Missing element(s): ${missing.join(', ')}`, container);
      states.delete(container);
      delete container.refresh;
      return;
    }
    Object.assign(state, elements);

    const handleInput = () => filterItems(state);
    const handleInputKeydown = (event) => handleKeyNavigation(event, state);
    const handleMenuMousemove = (event) => {
      const menuItem = event.target.closest('[role="menuitem"]');
      if (menuItem && state.visibleItems.includes(menuItem)) {
        const index = state.items.indexOf(menuItem);
        if (index !== state.activeIndex) setActiveItem(state, index);
      }
    };
    const handleMenuClick = (event) => {
      const clickedItem = event.target.closest('[role="menuitem"]');
      if (clickedItem && state.visibleItems.includes(clickedItem)) {
        const dialog = container.closest('dialog.command-dialog');
        if (dialog && !clickedItem.hasAttribute('data-keep-command-open')) dialog.close();
      }
    };

    state.input.addEventListener('input', handleInput);
    state.input.addEventListener('keydown', handleInputKeydown);
    state.menu.addEventListener('mousemove', handleMenuMousemove);
    state.menu.addEventListener('click', handleMenuClick);

    container._destroy = () => {
      state.input.removeEventListener('input', handleInput);
      state.input.removeEventListener('keydown', handleInputKeydown);
      state.menu.removeEventListener('mousemove', handleMenuMousemove);
      state.menu.removeEventListener('click', handleMenuClick);
      states.delete(container);
      delete container.refresh;
    };

    refreshCommand(container);
    container.dataset.commandInitialized = 'true';
    container.dispatchEvent(new CustomEvent('basecoat:initialized'));
  };

  if (window.basecoat) {
    window.basecoat.register('command', {
      selector: '.command:not([data-command-initialized])',
      init: initCommand,
      refresh: refreshCommand,
    });
  }
})();

(() => {
  const states = new WeakMap();

  const getElements = (root) => {
    const popover = root.querySelector(':scope > [data-popover]');
    const input = root.querySelector(':scope > input[role="combobox"], :scope > .input-group input[role="combobox"], :scope > .combobox-chips input[role="combobox"]')
      || popover?.querySelector('input[role="combobox"]');
    const chips = root.querySelector(':scope > .combobox-chips');
    const popupTrigger = root.querySelector(':scope > button[aria-haspopup="listbox"]');
    const trigger = popupTrigger || root.querySelector(':scope > .input-group button[aria-haspopup="listbox"]');
    const clearButton = root.querySelector('[data-clear]');
    const valueTarget = popupTrigger?.querySelector('[data-value]') || (popupTrigger?.matches('[data-value]') ? popupTrigger : null);
    const listbox = popover ? popover.querySelector('[role="listbox"]') : null;
    const hiddenInput = root.querySelector(':scope > input[type="hidden"]');
    return { input, chips, trigger, clearButton, valueTarget, popover, listbox, hiddenInput };
  };

  const ensureMultipleInputSurface = (root, elements) => {
    if (elements.listbox?.getAttribute('aria-multiselectable') !== 'true' || elements.chips || !elements.input) return elements;
    if (elements.input.parentElement !== root) return elements;

    const chips = document.createElement('div');
    chips.className = 'combobox-chips';
    root.insertBefore(chips, elements.input);
    chips.appendChild(elements.input);

    return getElements(root);
  };

  const getValue = option => option.dataset.value ?? option.textContent.trim();
  const getLabel = option => option.dataset.label || option.textContent.trim();
  const getFormat = root => root.dataset.format === 'object' ? 'object' : 'value';
  const isDisabled = option => option.getAttribute('aria-disabled') === 'true';
  const toSelected = option => ({ value: getValue(option), label: getLabel(option) });
  const normalizeEntry = entry => {
    if (entry && typeof entry === 'object') {
      const value = entry.value == null ? '' : String(entry.value);
      return value ? { value, label: String(entry.label ?? entry.value) } : null;
    }
    const value = entry == null ? '' : String(entry);
    return value ? { value, label: value } : null;
  };

  const getOptions = (listbox) => {
    const allOptions = Array.from(listbox.querySelectorAll('[role="option"]'));
    return {
      allOptions,
      options: allOptions.filter(option => !isDisabled(option)),
    };
  };

  const getSelection = (state) => Array.from(state.selected.values());
  const getCanonicalValue = (state) => state.isMultiple ? getSelection(state).map(item => item.value) : (getSelection(state)[0]?.value || '');
  const getSelectedDetail = (state) => state.isMultiple ? getSelection(state) : (getSelection(state)[0] || null);

  const serializeSelection = (state) => {
    const selected = getSelection(state);
    if (state.format === 'object') {
      return JSON.stringify(state.isMultiple ? selected : (selected[0] || null));
    }
    const value = selected.map(item => item.value);
    return state.isMultiple ? JSON.stringify(value) : (value[0] || '');
  };

  const parseStoredSelection = (storedValue, inputValue, state) => {
    if (state.isMultiple) {
      let parsed = [];
      try {
        parsed = JSON.parse(storedValue || '[]');
      } catch (_) {
        parsed = [];
      }
      if (!Array.isArray(parsed)) return [];
      return parsed.map(item => {
        const entry = normalizeEntry(state.format === 'object' ? item : { value: item, label: state.selected.get(String(item))?.label ?? item });
        if (!entry) return null;
        const option = state.options.find(opt => getValue(opt) === entry.value);
        return option ? toSelected(option) : entry;
      }).filter(Boolean);
    }

    if (state.format === 'object') {
      try {
        const entry = normalizeEntry(JSON.parse(storedValue || 'null'));
        if (!entry) return [];
        const option = state.options.find(opt => getValue(opt) === entry.value);
        return [option ? toSelected(option) : entry];
      } catch (_) {
        return [];
      }
    }

    const value = storedValue || '';
    if (!value) return [];
    const option = state.options.find(opt => getValue(opt) === value);
    return [option ? toSelected(option) : { value, label: state.selected.get(value)?.label || inputValue || value }];
  };

  const scrollOptionIntoListbox = (state, option) => {
    const optionRect = option.getBoundingClientRect();
    const listboxRect = state.listbox.getBoundingClientRect();

    if (optionRect.top < listboxRect.top) {
      state.listbox.scrollTop -= listboxRect.top - optionRect.top;
    } else if (optionRect.bottom > listboxRect.bottom) {
      state.listbox.scrollTop += optionRect.bottom - listboxRect.bottom;
    }
  };

  const setActiveOption = (state, index) => {
    if (state.activeIndex > -1 && state.options[state.activeIndex]) {
      state.options[state.activeIndex].classList.remove('active');
    }

    state.activeIndex = index;

    if (state.activeIndex > -1) {
      const activeOption = state.options[state.activeIndex];
      activeOption.classList.add('active');
      if (!activeOption.id) activeOption.id = `${state.listbox.id || state.root.id || 'combobox'}-option-${state.activeIndex}`;
      state.input.setAttribute('aria-activedescendant', activeOption.id);
    } else {
      state.input.removeAttribute('aria-activedescendant');
    }
  };

  const syncEmptyState = (state) => {
    state.popover.dataset.empty = String(state.visibleOptions.length === 0);
  };

  const filterOptions = (state, { preserveActive = false, search: forcedSearch } = {}) => {
    const previousActive = state.activeIndex > -1 ? state.options[state.activeIndex] : null;
    state.visibleOptions = [];

    if (state.manualFilter) {
      state.visibleOptions = state.options.filter(option => option.getAttribute('aria-hidden') !== 'true');

      if (preserveActive && previousActive && state.visibleOptions.includes(previousActive)) {
        setActiveOption(state, state.options.indexOf(previousActive));
      } else {
        setActiveOption(state, state.autoHighlight && state.visibleOptions.length > 0 ? state.options.indexOf(state.visibleOptions[0]) : -1);
      }
      syncEmptyState(state);
      return;
    }

    const search = (forcedSearch ?? state.input.value).trim().toLowerCase();

    state.allOptions.forEach(option => {
      if (option.hasAttribute('data-force')) {
        option.setAttribute('aria-hidden', 'false');
        if (state.options.includes(option)) state.visibleOptions.push(option);
        return;
      }

      const optionText = (option.dataset.filter || option.dataset.label || option.textContent).trim().toLowerCase();
      const keywords = (option.dataset.keywords || '').toLowerCase().split(/[\s,]+/).filter(Boolean);
      const matches = !search || optionText.includes(search) || keywords.some(keyword => keyword.includes(search));
      option.setAttribute('aria-hidden', String(!matches));
      if (matches && state.options.includes(option)) state.visibleOptions.push(option);
    });

    if (preserveActive && previousActive && state.visibleOptions.includes(previousActive)) {
      setActiveOption(state, state.options.indexOf(previousActive));
    } else {
      setActiveOption(state, state.autoHighlight && state.visibleOptions.length > 0 ? state.options.indexOf(state.visibleOptions[0]) : -1);
    }
    syncEmptyState(state);
  };

  const renderChips = (root) => {
    const state = states.get(root);
    if (!state.chips) return;

    state.chips.querySelectorAll('.combobox-chip').forEach(chip => chip.remove());

    getSelection(state).forEach(entry => {
      const chip = document.createElement('span');
      chip.className = 'combobox-chip';
      chip.dataset.value = entry.value;

      const label = document.createElement('span');
      label.textContent = entry.label;

      const remove = document.createElement('button');
      remove.type = 'button';
      remove.className = 'combobox-chip-remove btn';
      remove.dataset.variant = 'ghost';
      remove.dataset.size = 'icon-xs';
      remove.setAttribute('aria-label', `Remove ${entry.label}`);
      remove.innerHTML = '<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>';
      remove.addEventListener('click', (event) => {
        event.stopPropagation();
        root.deselect(entry.value);
        state.input.focus();
      });

      chip.appendChild(label);
      chip.appendChild(remove);
      state.chips.insertBefore(chip, state.input);
    });
  };

  const syncTriggerValue = (state) => {
    if (!state.valueTarget) return;
    const selected = getSelection(state);
    state.valueTarget.textContent = state.isMultiple
      ? selected.map(entry => entry.label).join(', ')
      : (selected[0]?.label || state.valueTarget.dataset.placeholder || '');
  };

  const syncClearButton = (state) => {
    if (!state.clearButton) return;
    state.clearButton.hidden = getSelection(state).length === 0 && state.input.value === '';
  };

  const syncSelectedOptions = (state) => {
    state.options.forEach(option => {
      if (state.selected.has(getValue(option))) {
        option.setAttribute('aria-selected', 'true');
      } else {
        option.removeAttribute('aria-selected');
      }
    });
  };

  const setSelected = (root, entries, triggerEvent = true) => {
    const state = states.get(root);
    const normalized = (Array.isArray(entries) ? entries : [entries]).map(normalizeEntry).filter(Boolean);

    state.selected.clear();
    if (state.isMultiple) {
      normalized.forEach(entry => state.selected.set(entry.value, entry));
      state.input.value = '';
    } else if (normalized[0]) {
      state.selected.set(normalized[0].value, normalized[0]);
      state.input.value = state.valueTarget ? '' : normalized[0].label;
    } else {
      state.input.value = '';
    }

    state.hiddenInput.value = serializeSelection(state);
    syncSelectedOptions(state);
    if (state.isMultiple) {
      renderChips(root);
      filterOptions(state, { preserveActive: true });
    }
    syncTriggerValue(state);
    syncClearButton(state);

    if (triggerEvent) {
      root.dispatchEvent(new CustomEvent('change', {
        detail: { value: getCanonicalValue(state), selected: getSelectedDetail(state) },
        bubbles: true,
      }));
    }
  };

  const closePopover = (state, focusInput = false) => {
    if (state.popover.getAttribute('aria-hidden') === 'true') return;
    state.popover.setAttribute('aria-hidden', 'true');
    state.input.setAttribute('aria-expanded', 'false');
    state.trigger?.setAttribute('aria-expanded', 'false');
    setActiveOption(state, -1);
    if (focusInput) {
      state.skipOpenOnFocus = true;
      state.input.focus();
      requestAnimationFrame(() => { state.skipOpenOnFocus = false; });
    }
  };

  const openPopover = (state) => {
    const { root } = state;
    if (state.popover.getAttribute('aria-hidden') === 'false') return;
    document.dispatchEvent(new CustomEvent('basecoat:popover', { detail: { source: root } }));
    root.refresh();
    const selected = getSelection(state)[0];
    if (state.valueTarget) state.input.value = '';
    const search = !state.isMultiple && selected && state.input.value === selected.label ? '' : undefined;
    filterOptions(state, { search });
    state.popover.setAttribute('aria-hidden', 'false');
    state.input.setAttribute('aria-expanded', 'true');
    state.trigger?.setAttribute('aria-expanded', 'true');
    if (state.trigger) state.input.focus();
  };

  const refreshCombobox = (root) => {
    const state = states.get(root);
    if (!state) return;

    let elements = getElements(root);
    if (!elements.input || !elements.popover || !elements.listbox || !elements.hiddenInput) {
      const missing = [];
      if (!elements.input) missing.push('input');
      if (!elements.popover) missing.push('popover');
      if (!elements.listbox) missing.push('listbox');
      if (!elements.hiddenInput) missing.push('hidden input');
      console.error(`Combobox refresh failed. Missing element(s): ${missing.join(', ')}`, root);
      return;
    }

    elements = ensureMultipleInputSurface(root, elements);
    const previousValue = elements.hiddenInput.value;
    const previousInputValue = elements.input.value;
    Object.assign(state, elements, getOptions(elements.listbox));
    state.isMultiple = state.listbox.getAttribute('aria-multiselectable') === 'true';
    state.closeOnSelect = root.dataset.closeOnSelect === 'true';
    state.autoHighlight = root.dataset.autoHighlight === 'true';
    state.manualFilter = root.dataset.filter === 'manual';
    state.format = getFormat(root);

    const stored = parseStoredSelection(previousValue, previousInputValue, state);
    if (stored.length > 0) {
      setSelected(root, stored, false);
    } else {
      const selectedOptions = state.options.filter(option => option.getAttribute('aria-selected') === 'true').map(toSelected);
      if (selectedOptions.length > 0) {
        setSelected(root, state.isMultiple ? selectedOptions : selectedOptions[0], false);
      } else if (state.isMultiple) {
        setSelected(root, [], false);
      } else {
        state.input.value = previousInputValue;
        state.selected.clear();
        state.hiddenInput.value = '';
        syncSelectedOptions(state);
        filterOptions(state, { preserveActive: true });
      }
    }
    syncTriggerValue(state);
    syncClearButton(state);
  };

  const resolveEntry = (state, value) => {
    if (value && typeof value === 'object') {
      const entry = normalizeEntry(value);
      if (!entry) return null;
      const option = state.options.find(opt => getValue(opt) === entry.value);
      return option ? toSelected(option) : entry;
    }

    const option = state.options.find(opt => getValue(opt) === value);
    if (option) return toSelected(option);
    const existing = state.selected.get(String(value));
    return existing || null;
  };

  const selectValue = (root, value) => {
    const state = states.get(root);
    const entry = resolveEntry(state, value);
    if (!entry) return;

    if (state.isMultiple) {
      setSelected(root, [...getSelection(state).filter(item => item.value !== entry.value), entry]);
      if (state.closeOnSelect) root.close(true);
    } else {
      setSelected(root, entry);
      root.close(state.trigger ? false : true);
      state.trigger?.focus();
    }
  };

  const deselectValue = (root, value) => {
    const state = states.get(root);
    if (!state.isMultiple) return;
    const normalized = String(value);
    if (!state.selected.has(normalized)) return;
    setSelected(root, getSelection(state).filter(item => item.value !== normalized));
  };

  const clearValue = (root) => {
    const state = states.get(root);
    setSelected(root, [], true);
    state.input.value = '';
    filterOptions(state);
    syncClearButton(state);
    if (state.popover.getAttribute('aria-hidden') === 'false') state.input.focus();
  };

  const handleKeydown = (event, root) => {
    const state = states.get(root);
    if (!['ArrowDown', 'ArrowUp', 'Enter', 'Home', 'End', 'Escape', 'Backspace'].includes(event.key)) return;

    const isOpen = state.popover.getAttribute('aria-hidden') === 'false';

    if (event.key === 'Backspace' && state.isMultiple && state.input.value === '') {
      const selected = getSelection(state);
      const last = selected[selected.length - 1];
      if (last) root.deselect(last.value);
      return;
    }

    if (event.key === 'Escape') {
      root.close(true);
      return;
    }

    if (!isOpen && ['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
      event.preventDefault();
      openPopover(state);
    }

    if (state.popover.getAttribute('aria-hidden') === 'true') return;

    if (event.key === 'Enter') {
      if (state.activeIndex > -1) {
        event.preventDefault();
        const option = state.options[state.activeIndex];
        state.isMultiple ? root.toggle(getValue(option)) : root.select(getValue(option));
      }
      return;
    }

    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key) || state.visibleOptions.length === 0) return;
    event.preventDefault();

    const currentVisibleIndex = state.activeIndex > -1 ? state.visibleOptions.indexOf(state.options[state.activeIndex]) : -1;
    let nextVisibleIndex = currentVisibleIndex;

    if (event.key === 'ArrowDown') nextVisibleIndex = Math.min(currentVisibleIndex + 1, state.visibleOptions.length - 1);
    if (event.key === 'ArrowUp') nextVisibleIndex = currentVisibleIndex <= 0 ? 0 : currentVisibleIndex - 1;
    if (event.key === 'Home') nextVisibleIndex = 0;
    if (event.key === 'End') nextVisibleIndex = state.visibleOptions.length - 1;

    const nextOption = state.visibleOptions[nextVisibleIndex];
    setActiveOption(state, state.options.indexOf(nextOption));
    scrollOptionIntoListbox(state, nextOption);
  };

  const initCombobox = (root) => {
    if (root.dataset.comboboxInitialized) return;

    const state = { root, activeIndex: -1, allOptions: [], options: [], visibleOptions: [], selected: new Map(), format: 'value', manualFilter: false, skipOpenOnFocus: false };
    states.set(root, state);
    root.refresh = () => refreshCombobox(root);

    refreshCombobox(root);
    if (!state.input || !state.popover || !state.listbox || !state.hiddenInput) {
      states.delete(root);
      delete root.refresh;
      return;
    }

    root.close = (focusInput = false) => closePopover(state, focusInput);

    root.select = (value) => selectValue(root, value);
    root.selectByValue = root.select;
    root.setValue = (value) => {
      const entries = state.isMultiple ? (Array.isArray(value) ? value : (value == null ? [] : [value])) : [value];
      const resolved = entries.map(entry => resolveEntry(state, entry) || normalizeEntry(entry)).filter(Boolean);
      setSelected(root, state.isMultiple ? resolved : resolved[0]);
    };
    if (state.isMultiple) {
      root.deselect = (value) => deselectValue(root, value);
      root.toggle = (value) => {
        const entry = resolveEntry(state, value);
        if (!entry) return;
        state.selected.has(entry.value) ? root.deselect(entry.value) : root.select(entry);
      };
      root.selectAll = () => setSelected(root, state.options.map(toSelected));
      root.selectNone = () => setSelected(root, []);
    }

    const handleInputFocus = () => {
      if (state.skipOpenOnFocus) return;
      openPopover(state);
    };
    const handleInputClick = () => openPopover(state);
    const handleInput = () => {
      openPopover(state);
      filterOptions(state);
      if (!state.isMultiple) {
        state.hiddenInput.value = '';
        state.selected.clear();
        syncSelectedOptions(state);
        syncTriggerValue(state);
        syncClearButton(state);
      }
    };
    const handleTriggerClick = () => {
      if (state.popover.getAttribute('aria-hidden') === 'false') {
        root.close(false);
      } else {
        openPopover(state);
      }
    };
    const handleClearClick = (event) => {
      event.preventDefault();
      event.stopPropagation();
      clearValue(root);
    };
    const handleInputKeydown = (event) => handleKeydown(event, root);
    const handleListboxMousemove = (event) => {
      const option = event.target.closest('[role="option"]');
      if (option && state.visibleOptions.includes(option)) setActiveOption(state, state.options.indexOf(option));
    };
    const handleListboxClick = (event) => {
      const option = event.target.closest('[role="option"]');
      if (!option || !state.options.includes(option)) return;
      state.isMultiple ? root.toggle(getValue(option)) : root.select(getValue(option));
      if (state.isMultiple && !state.closeOnSelect) state.input.focus();
    };
    const handleDocumentClick = (event) => {
      if (!root.contains(event.target)) root.close(false);
    };
    const handleDocumentPopover = (event) => {
      if (event.detail.source !== root) root.close(false);
    };

    state.input.addEventListener('focus', handleInputFocus);
    state.input.addEventListener('click', handleInputClick);
    state.input.addEventListener('input', handleInput);
    state.input.addEventListener('keydown', handleInputKeydown);
    state.trigger?.addEventListener('click', handleTriggerClick);
    state.clearButton?.addEventListener('click', handleClearClick);
    state.listbox.addEventListener('mousemove', handleListboxMousemove);
    state.listbox.addEventListener('click', handleListboxClick);
    document.addEventListener('click', handleDocumentClick);
    document.addEventListener('basecoat:popover', handleDocumentPopover);

    root._destroy = () => {
      state.input.removeEventListener('focus', handleInputFocus);
      state.input.removeEventListener('click', handleInputClick);
      state.input.removeEventListener('input', handleInput);
      state.input.removeEventListener('keydown', handleInputKeydown);
      state.trigger?.removeEventListener('click', handleTriggerClick);
      state.clearButton?.removeEventListener('click', handleClearClick);
      state.listbox.removeEventListener('mousemove', handleListboxMousemove);
      state.listbox.removeEventListener('click', handleListboxClick);
      document.removeEventListener('click', handleDocumentClick);
      document.removeEventListener('basecoat:popover', handleDocumentPopover);
      state.chips?.querySelectorAll('.combobox-chip').forEach(chip => chip.remove());
      states.delete(root);
      delete root.refresh;
      delete root.close;
      delete root.select;
      delete root.selectByValue;
      delete root.setValue;
      delete root.clear;
      delete root.deselect;
      delete root.toggle;
      delete root.selectAll;
      delete root.selectNone;
    };

    state.popover.setAttribute('aria-hidden', 'true');
    state.input.setAttribute('aria-expanded', 'false');
    state.trigger?.setAttribute('aria-expanded', 'false');
    root.clear = () => clearValue(root);

    Object.defineProperty(root, 'value', {
      configurable: true,
      get: () => getCanonicalValue(state),
      set: (value) => root.setValue(value),
    });

    Object.defineProperty(root, 'selected', {
      configurable: true,
      get: () => getSelectedDetail(state),
    });

    root.dataset.comboboxInitialized = 'true';
    root.dispatchEvent(new CustomEvent('basecoat:initialized'));
  };

  if (window.basecoat) {
    window.basecoat.register('combobox', {
      selector: '.combobox:not([data-combobox-initialized])',
      init: initCombobox,
      refresh: refreshCombobox,
    });
  }
})();

(() => {
  const toMs = (value) => {
    if (!value) return 0;
    const trimmed = value.trim();
    if (trimmed.endsWith('ms')) return parseFloat(trimmed) || 0;
    if (trimmed.endsWith('s')) return (parseFloat(trimmed) || 0) * 1000;
    return parseFloat(trimmed) || 0;
  };

  const maxTransitionMs = (element) => {
    if (!element) return 0;
    const styles = getComputedStyle(element);
    const durations = styles.transitionDuration.split(',').map(toMs);
    const delays = styles.transitionDelay.split(',').map(toMs);
    return Math.max(0, ...durations.map((duration, index) => duration + (delays[index] || delays[0] || 0)));
  };

  const initDrawer = (drawer) => {
    if (drawer.dataset.drawerInitialized) return;

    const nativeClose = drawer.close.bind(drawer);
    let pointerStartedOnBackdrop = false;
    let closeTimer = null;

    const finishClose = (returnValue) => {
      window.clearTimeout(closeTimer);
      closeTimer = null;
      drawer.removeAttribute('data-closing');
      nativeClose(returnValue);
    };

    drawer.close = (returnValue = '') => {
      if (!drawer.open) return;
      if (drawer.dataset.closing) return;

      const content = drawer.firstElementChild;
      const duration = maxTransitionMs(content);

      if (duration === 0 || window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
        finishClose(returnValue);
        return;
      }

      drawer.dataset.closing = 'true';
      closeTimer = window.setTimeout(() => finishClose(returnValue), duration + 50);
    };

    const handleCancel = (event) => {
      event.preventDefault();
      drawer.close();
    };

    const handlePointerDown = (event) => {
      pointerStartedOnBackdrop = event.target === drawer;
    };

    const handlePointerUp = (event) => {
      if (pointerStartedOnBackdrop && event.target === drawer) {
        drawer.close();
      }
      pointerStartedOnBackdrop = false;
    };

    drawer.addEventListener('cancel', handleCancel);
    drawer.addEventListener('pointerdown', handlePointerDown);
    drawer.addEventListener('pointerup', handlePointerUp);

    drawer._destroy = () => {
      window.clearTimeout(closeTimer);
      drawer.removeEventListener('cancel', handleCancel);
      drawer.removeEventListener('pointerdown', handlePointerDown);
      drawer.removeEventListener('pointerup', handlePointerUp);
      drawer.close = nativeClose;
      delete drawer._destroy;
    };

    drawer.dataset.drawerInitialized = 'true';
    drawer.dispatchEvent(new CustomEvent('basecoat:initialized'));
  };

  if (window.basecoat) {
    window.basecoat.register('drawer', '.drawer:not([data-drawer-initialized])', initDrawer);
  }
})();

(() => {
  const states = new WeakMap();

  const isDisabled = (item) =>
    item.hasAttribute('disabled') || item.getAttribute('aria-disabled') === 'true';

  const getElements = (root) => {
    const trigger = root.querySelector(':scope > button');
    const popover = root.querySelector(':scope > [data-popover]');
    const menu = popover ? popover.querySelector('[role="menu"]') : null;
    return { trigger, popover, menu };
  };

  const getItems = (menu) => Array.from(menu.querySelectorAll('[role^="menuitem"]')).filter(item => !isDisabled(item));

  const setActiveItem = (state, index) => {
    if (state.activeIndex > -1 && state.items[state.activeIndex]) {
      state.items[state.activeIndex].classList.remove('active');
    }
    state.activeIndex = index;
    if (state.activeIndex > -1 && state.items[state.activeIndex]) {
      const activeItem = state.items[state.activeIndex];
      activeItem.classList.add('active');
      if (activeItem.id) state.trigger.setAttribute('aria-activedescendant', activeItem.id);
    } else {
      state.trigger.removeAttribute('aria-activedescendant');
    }
  };

  const refreshDropdownMenu = (root) => {
    const state = states.get(root);
    if (!state) return;

    const elements = getElements(root);
    if (!elements.trigger || !elements.popover || !elements.menu) {
      const missing = [];
      if (!elements.trigger) missing.push('trigger');
      if (!elements.popover) missing.push('popover');
      if (!elements.menu) missing.push('menu');
      console.error(`Dropdown menu refresh failed. Missing element(s): ${missing.join(', ')}`, root);
      return;
    }

    Object.assign(state, elements);
    state.items = getItems(state.menu);
    if (state.activeIndex > -1 && !state.items[state.activeIndex]) setActiveItem(state, -1);
  };

  const closePopover = (state, focusOnTrigger = true) => {
    if (state.trigger.getAttribute('aria-expanded') === 'false') return;
    state.trigger.setAttribute('aria-expanded', 'false');
    state.trigger.removeAttribute('aria-activedescendant');
    state.popover.setAttribute('aria-hidden', 'true');
    if (focusOnTrigger) state.trigger.focus();
    setActiveItem(state, -1);
  };

  const openPopover = (root, state, initialSelection = false) => {
    document.dispatchEvent(new CustomEvent('basecoat:popover', { detail: { source: root } }));
    root.refresh();
    state.trigger.setAttribute('aria-expanded', 'true');
    state.popover.setAttribute('aria-hidden', 'false');

    if (state.items.length > 0 && initialSelection) {
      setActiveItem(state, initialSelection === 'last' ? state.items.length - 1 : 0);
    }
  };

  const initDropdownMenu = (root) => {
    if (root.dataset.dropdownMenuInitialized) return;

    const state = { activeIndex: -1, items: [] };
    states.set(root, state);
    root.refresh = () => refreshDropdownMenu(root);

    refreshDropdownMenu(root);
    if (!state.trigger || !state.popover || !state.menu) {
      states.delete(root);
      delete root.refresh;
      return;
    }

    root.open = (initialSelection = false) => openPopover(root, state, initialSelection);
    root.close = (focusOnTrigger = true) => closePopover(state, focusOnTrigger);
    root.toggle = () => state.trigger.getAttribute('aria-expanded') === 'true' ? root.close() : root.open(false);

    const handleTriggerClick = root.toggle;

    const handleKeydown = (event) => {
      const isExpanded = state.trigger.getAttribute('aria-expanded') === 'true';

      if (event.key === 'Escape') {
        if (isExpanded) root.close();
        return;
      }

      if (!isExpanded) {
        if (['Enter', ' '].includes(event.key)) {
          event.preventDefault();
          root.open(false);
        } else if (event.key === 'ArrowDown') {
          event.preventDefault();
          root.open('first');
        } else if (event.key === 'ArrowUp') {
          event.preventDefault();
          root.open('last');
        }
        return;
      }

      if (state.items.length === 0) return;

      let nextIndex = state.activeIndex;
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        nextIndex = state.activeIndex === -1 ? 0 : Math.min(state.activeIndex + 1, state.items.length - 1);
      } else if (event.key === 'ArrowUp') {
        event.preventDefault();
        nextIndex = state.activeIndex === -1 ? state.items.length - 1 : Math.max(state.activeIndex - 1, 0);
      } else if (event.key === 'Home') {
        event.preventDefault();
        nextIndex = 0;
      } else if (event.key === 'End') {
        event.preventDefault();
        nextIndex = state.items.length - 1;
      } else if (['Enter', ' '].includes(event.key)) {
        event.preventDefault();
        state.items[state.activeIndex]?.click();
        root.close();
        return;
      } else {
        return;
      }

      if (nextIndex !== state.activeIndex) setActiveItem(state, nextIndex);
    };

    const handleMenuMousemove = (event) => {
      const menuItem = event.target.closest('[role^="menuitem"]');
      if (menuItem && !isDisabled(menuItem) && state.items.includes(menuItem)) {
        const index = state.items.indexOf(menuItem);
        if (index !== state.activeIndex) setActiveItem(state, index);
      }
    };

    const handleMenuMouseleave = () => setActiveItem(state, -1);
    const handleMenuClick = (event) => {
      const menuItem = event.target.closest('[role^="menuitem"]');
      if (!menuItem || isDisabled(menuItem)) return;

      if (menuItem.getAttribute('role') === 'menuitemcheckbox') {
        menuItem.setAttribute('aria-checked', menuItem.getAttribute('aria-checked') !== 'true');
      } else if (menuItem.getAttribute('role') === 'menuitemradio') {
        const group = menuItem.closest('[role="group"], [role="menu"]');
        group?.querySelectorAll('[role="menuitemradio"]').forEach((item) => {
          if (!isDisabled(item)) item.setAttribute('aria-checked', item === menuItem ? 'true' : 'false');
        });
      }

      root.close();
    };

    const handleDocumentClick = (event) => {
      if (!root.contains(event.target)) root.close(false);
    };

    const handleDocumentPopover = (event) => {
      if (event.detail.source !== root) root.close(false);
    };

    state.trigger.addEventListener('click', handleTriggerClick);
    root.addEventListener('keydown', handleKeydown);
    state.menu.addEventListener('mousemove', handleMenuMousemove);
    state.menu.addEventListener('mouseleave', handleMenuMouseleave);
    state.menu.addEventListener('click', handleMenuClick);
    document.addEventListener('click', handleDocumentClick);
    document.addEventListener('basecoat:popover', handleDocumentPopover);

    root._destroy = () => {
      state.trigger.removeEventListener('click', handleTriggerClick);
      root.removeEventListener('keydown', handleKeydown);
      state.menu.removeEventListener('mousemove', handleMenuMousemove);
      state.menu.removeEventListener('mouseleave', handleMenuMouseleave);
      state.menu.removeEventListener('click', handleMenuClick);
      document.removeEventListener('click', handleDocumentClick);
      document.removeEventListener('basecoat:popover', handleDocumentPopover);
      states.delete(root);
      delete root.refresh;
      delete root.open;
      delete root.close;
      delete root.toggle;
    };

    state.trigger.setAttribute('aria-expanded', 'false');
    state.popover.setAttribute('aria-hidden', 'true');
    root.dataset.dropdownMenuInitialized = 'true';
    root.dispatchEvent(new CustomEvent('basecoat:initialized'));
  };

  if (window.basecoat) {
    window.basecoat.register('dropdown-menu', {
      selector: '.dropdown-menu:not([data-dropdown-menu-initialized])',
      init: initDropdownMenu,
      refresh: refreshDropdownMenu,
    });
  }
})();

(() => {
  const initPopover = (popoverComponent) => {
    if (popoverComponent.dataset.popoverInitialized) return;

    const trigger = popoverComponent.querySelector(':scope > button');
    const content = popoverComponent.querySelector(':scope > [data-popover]');

    if (!trigger || !content) {
      const missing = [];
      if (!trigger) missing.push('trigger');
      if (!content) missing.push('content');
      console.error(`Popover initialisation failed. Missing element(s): ${missing.join(', ')}`, popoverComponent);
      return;
    }

    const closePopover = (focusOnTrigger = true) => {
      if (trigger.getAttribute('aria-expanded') === 'false') return;
      trigger.setAttribute('aria-expanded', 'false');
      content.setAttribute('aria-hidden', 'true');
      if (focusOnTrigger) {
        trigger.focus();
      }
    };

    const openPopover = () => {
      document.dispatchEvent(new CustomEvent('basecoat:popover', {
        detail: { source: popoverComponent }
      }));
      
      const elementToFocus = content.querySelector('[autofocus]');
      if (elementToFocus) {
        content.addEventListener('transitionend', () => {
          elementToFocus.focus();
        }, { once: true });
      }

      trigger.setAttribute('aria-expanded', 'true');
      content.setAttribute('aria-hidden', 'false');
    };

    const handleTriggerClick = () => {
      const isExpanded = trigger.getAttribute('aria-expanded') === 'true';
      if (isExpanded) {
        closePopover();
      } else {
        openPopover();
      }
    };

    const handleKeydown = (event) => {
      if (event.key === 'Escape') {
        closePopover();
      }
    };

    const handleDocumentClick = (event) => {
      if (!popoverComponent.contains(event.target)) {
        closePopover();
      }
    };

    const handleDocumentPopover = (event) => {
      if (event.detail.source !== popoverComponent) {
        closePopover(false);
      }
    };

    trigger.addEventListener('click', handleTriggerClick);
    popoverComponent.addEventListener('keydown', handleKeydown);
    document.addEventListener('click', handleDocumentClick);
    document.addEventListener('basecoat:popover', handleDocumentPopover);

    popoverComponent._destroy = () => {
      trigger.removeEventListener('click', handleTriggerClick);
      popoverComponent.removeEventListener('keydown', handleKeydown);
      document.removeEventListener('click', handleDocumentClick);
      document.removeEventListener('basecoat:popover', handleDocumentPopover);
    };

    popoverComponent.dataset.popoverInitialized = true;
    popoverComponent.dispatchEvent(new CustomEvent('basecoat:initialized'));
  };

  if (window.basecoat) {
    window.basecoat.register('popover', '.popover:not([data-popover-initialized])', initPopover);
  }
})();

(() => {
  function updateRange(element) {
    const min = parseFloat(element.min || '0');
    const max = parseFloat(element.max || '100');
    const value = parseFloat(element.value || '0');
    const percent = max === min ? 0 : ((value - min) / (max - min)) * 100;
    element.style.setProperty('--slider-value', `${percent}%`);
  }

  function initRange(element) {
    if (element.dataset.rangeInitialized) return;

    updateRange(element);
    const handleInput = () => updateRange(element);
    element.addEventListener('input', handleInput);
    element._destroy = () => {
      element.removeEventListener('input', handleInput);
    };
    element.dataset.rangeInitialized = 'true';
  }

  if (window.basecoat) {
    window.basecoat.register('range', 'input[type="range"]:not([data-range-initialized])', initRange);
  }
})();

(() => {
  const states = new WeakMap();

  const getElements = (root) => {
    const trigger = root.querySelector(':scope > button');
    const selectedLabel = trigger?.querySelector(':scope > span') || null;
    const popover = root.querySelector(':scope > [data-popover]');
    const listbox = popover ? popover.querySelector('[role="listbox"]') : null;
    const input = root.querySelector(':scope > input[type="hidden"]');
    return { trigger, selectedLabel, popover, listbox, input };
  };

  const getValue = (option) => option.dataset.value ?? option.textContent.trim();
  const getLabel = (option) => option.dataset.label || option.textContent.trim();
  const getFormat = (root) => root.dataset.format === 'object' ? 'object' : 'value';
  const isDisabled = (option) => option.getAttribute('aria-disabled') === 'true';
  const toSelected = (option) => ({ value: getValue(option), label: getLabel(option) });

  const getOptions = (listbox) => {
    const allOptions = Array.from(listbox.querySelectorAll('[role="option"]'));
    return {
      allOptions,
      options: allOptions.filter(option => !isDisabled(option)),
    };
  };

  const parseStoredValues = (storedValue, { isMultiple, format }) => {
    if (isMultiple) {
      try {
        const parsed = JSON.parse(storedValue || '[]');
        if (!Array.isArray(parsed)) return [];
        return parsed
          .map(item => format === 'object' && item && typeof item === 'object' ? item.value : item)
          .filter(value => value != null)
          .map(String);
      } catch (_) {
        return [];
      }
    }

    if (format === 'object') {
      try {
        const parsed = JSON.parse(storedValue || 'null');
        return parsed && typeof parsed === 'object' && parsed.value != null ? String(parsed.value) : '';
      } catch (_) {
        return '';
      }
    }

    return storedValue || '';
  };

  const serializeSelection = (state, selected) => {
    if (state.format === 'object') {
      return JSON.stringify(state.isMultiple ? selected : (selected[0] || null));
    }

    const value = selected.map(item => item.value);
    return state.isMultiple ? JSON.stringify(value) : (value[0] || '');
  };

  const showPlaceholder = (state) => {
    state.selectedLabel.textContent = state.placeholder || '';
    state.selectedLabel.classList.toggle('text-muted-foreground', Boolean(state.placeholder));
    state.input.value = state.isMultiple ? serializeSelection(state, []) : '';
  };

  const scrollOptionIntoListbox = (state, option) => {
    const optionRect = option.getBoundingClientRect();
    const listboxRect = state.listbox.getBoundingClientRect();

    if (optionRect.top < listboxRect.top) {
      state.listbox.scrollTop -= listboxRect.top - optionRect.top;
    } else if (optionRect.bottom > listboxRect.bottom) {
      state.listbox.scrollTop += optionRect.bottom - listboxRect.bottom;
    }
  };

  const setActiveOption = (state, index) => {
    if (state.activeIndex > -1 && state.options[state.activeIndex]) {
      state.options[state.activeIndex].classList.remove('active');
    }

    state.activeIndex = index;

    if (state.activeIndex > -1) {
      const activeOption = state.options[state.activeIndex];
      activeOption.classList.add('active');
      if (activeOption.id) {
        state.trigger.setAttribute('aria-activedescendant', activeOption.id);
      } else {
        state.trigger.removeAttribute('aria-activedescendant');
      }
    } else {
      state.trigger.removeAttribute('aria-activedescendant');
    }
  };

  const updateValue = (root, optionOrOptions, triggerEvent = true) => {
    const state = states.get(root);
    let value;
    let selectedDetail;

    if (state.isMultiple) {
      const selected = Array.isArray(optionOrOptions) ? optionOrOptions : [];
      state.selectedOptions.clear();
      selected.forEach(option => state.selectedOptions.add(option));

      const selectedInOrder = state.options.filter(option => state.selectedOptions.has(option));
      selectedDetail = selectedInOrder.map(toSelected);
      if (selectedInOrder.length === 0) {
        state.selectedLabel.textContent = state.placeholder;
        state.selectedLabel.classList.add('text-muted-foreground');
      } else {
        state.selectedLabel.textContent = selectedDetail.map(item => item.label).join(', ');
        state.selectedLabel.classList.remove('text-muted-foreground');
      }

      value = selectedDetail.map(item => item.value);
      state.input.value = serializeSelection(state, selectedDetail);
    } else {
      const option = optionOrOptions;
      if (!option) {
        state.options.forEach(option => option.removeAttribute('aria-selected'));
        showPlaceholder(state);
        selectedDetail = null;
        value = '';
      } else {
        if (option.dataset.label) {
          state.selectedLabel.textContent = option.dataset.label;
        } else {
          state.selectedLabel.innerHTML = option.innerHTML;
        }
        state.selectedLabel.classList.remove('text-muted-foreground');
        selectedDetail = toSelected(option);
        value = selectedDetail.value;
        state.input.value = serializeSelection(state, [selectedDetail]);
      }
    }

    state.options.forEach(option => {
      const isSelected = state.isMultiple ? state.selectedOptions.has(option) : optionOrOptions && option === optionOrOptions;
      if (isSelected) {
        option.setAttribute('aria-selected', 'true');
      } else {
        option.removeAttribute('aria-selected');
      }
    });

    if (triggerEvent) {
      root.dispatchEvent(new CustomEvent('change', {
        detail: { value, selected: selectedDetail },
        bubbles: true,
      }));
    }
  };

  const closePopover = (state, focusOnTrigger = true) => {
    if (state.popover.getAttribute('aria-hidden') === 'true') return;
    if (focusOnTrigger) state.trigger.focus();
    state.popover.setAttribute('aria-hidden', 'true');
    state.trigger.setAttribute('aria-expanded', 'false');
    setActiveOption(state, -1);
  };

  const refreshSelect = (root) => {
    const state = states.get(root);
    if (!state) return;

    const elements = getElements(root);
    if (!elements.trigger || !elements.selectedLabel || !elements.popover || !elements.listbox || !elements.input) {
      const missing = [];
      if (!elements.trigger) missing.push('trigger');
      if (!elements.selectedLabel) missing.push('selected label');
      if (!elements.popover) missing.push('popover');
      if (!elements.listbox) missing.push('listbox');
      if (!elements.input) missing.push('input');
      console.error(`Select component refresh failed. Missing element(s): ${missing.join(', ')}`, root);
      return;
    }

    const previousValue = elements.input.value;
    Object.assign(state, elements, getOptions(elements.listbox));
    state.visibleOptions = [...state.options];
    state.isMultiple = state.listbox.getAttribute('aria-multiselectable') === 'true';
    state.format = getFormat(root);
    state.placeholder = root.dataset.placeholder || '';
    state.closeOnSelect = root.dataset.closeOnSelect === 'true';

    if (state.isMultiple) {
      if (!state.selectedOptions) state.selectedOptions = new Set();
      const values = parseStoredValues(previousValue, state);
      const selected = values.length
        ? values.map(value => state.options.find(option => getValue(option) === value)).filter(Boolean)
        : state.options.filter(option => option.getAttribute('aria-selected') === 'true');
      updateValue(root, selected, false);
    } else {
      const value = parseStoredValues(previousValue, state);
      const selected = value === '' && state.placeholder
        ? null
        : state.options.find(option => getValue(option) === value)
        || state.options.find(option => option.getAttribute('aria-selected') === 'true');
      state.options.forEach(option => option.removeAttribute('aria-selected'));
      updateValue(root, selected || null, false);
    }

    const selectedOption = state.listbox.querySelector('[role="option"][aria-selected="true"]');
    setActiveOption(state, selectedOption ? state.options.indexOf(selectedOption) : -1);
  };

  const toggleMultipleValue = (root, option) => {
    const state = states.get(root);
    if (state.selectedOptions.has(option)) {
      state.selectedOptions.delete(option);
    } else {
      state.selectedOptions.add(option);
    }
    updateValue(root, state.options.filter(opt => state.selectedOptions.has(opt)));
  };

  const selectValue = (root, value) => {
    const state = states.get(root);
    if (state.isMultiple) {
      const option = state.options.find(opt => getValue(opt) === value && !state.selectedOptions.has(opt));
      if (!option) return;
      state.selectedOptions.add(option);
      updateValue(root, state.options.filter(opt => state.selectedOptions.has(opt)));
    } else {
      const option = state.options.find(opt => getValue(opt) === value);
      if (!option) return;
      if (state.placeholder && getValue(option) === '') {
        updateValue(root, null);
        closePopover(state);
        return;
      }
      if (root.value !== value) updateValue(root, option);
      closePopover(state);
    }
  };

  const deselectValue = (root, value) => {
    const state = states.get(root);
    if (!state.isMultiple) return;
    const option = state.options.find(opt => getValue(opt) === value && state.selectedOptions.has(opt));
    if (!option) return;
    state.selectedOptions.delete(option);
    updateValue(root, state.options.filter(opt => state.selectedOptions.has(opt)));
  };

  const handleKeyNavigation = (event, root) => {
    const state = states.get(root);
    const isPopoverOpen = state.popover.getAttribute('aria-hidden') === 'false';

    if (!['ArrowDown', 'ArrowUp', 'Enter', 'Home', 'End', 'Escape'].includes(event.key)) return;

    if (!isPopoverOpen) {
      if (event.key !== 'Enter' && event.key !== 'Escape') {
        event.preventDefault();
        root.open();
      }
      return;
    }

    event.preventDefault();

    if (event.key === 'Escape') {
      root.close();
      return;
    }

    if (event.key === 'Enter') {
      if (state.activeIndex > -1) {
        const option = state.options[state.activeIndex];
        if (state.isMultiple) {
          toggleMultipleValue(root, option);
          if (state.closeOnSelect) root.close();
        } else {
          if (state.placeholder && getValue(option) === '') {
            updateValue(root, null);
          } else if (root.value !== getValue(option)) {
            updateValue(root, option);
          }
          root.close();
        }
      }
      return;
    }

    if (state.visibleOptions.length === 0) return;

    const currentVisibleIndex = state.activeIndex > -1 ? state.visibleOptions.indexOf(state.options[state.activeIndex]) : -1;
    let nextVisibleIndex = currentVisibleIndex;

    if (event.key === 'ArrowDown' && currentVisibleIndex < state.visibleOptions.length - 1) nextVisibleIndex = currentVisibleIndex + 1;
    if (event.key === 'ArrowUp') nextVisibleIndex = currentVisibleIndex > 0 ? currentVisibleIndex - 1 : 0;
    if (event.key === 'Home') nextVisibleIndex = 0;
    if (event.key === 'End') nextVisibleIndex = state.visibleOptions.length - 1;

    if (nextVisibleIndex !== currentVisibleIndex) {
      const newActiveOption = state.visibleOptions[nextVisibleIndex];
      setActiveOption(state, state.options.indexOf(newActiveOption));
      scrollOptionIntoListbox(state, newActiveOption);
    }
  };

  const initSelect = (root) => {
    if (root.dataset.selectInitialized) return;

    const state = { activeIndex: -1, selectedOptions: null, options: [], allOptions: [], visibleOptions: [], format: 'value' };
    states.set(root, state);
    root.refresh = () => refreshSelect(root);

    refreshSelect(root);
    if (!state.trigger || !state.selectedLabel || !state.popover || !state.listbox || !state.input) {
      states.delete(root);
      delete root.refresh;
      return;
    }

    root.open = () => {
      document.dispatchEvent(new CustomEvent('basecoat:popover', { detail: { source: root } }));
      root.refresh();
      state.popover.setAttribute('aria-hidden', 'false');
      state.trigger.setAttribute('aria-expanded', 'true');

      const selectedOption = state.listbox.querySelector('[role="option"][aria-selected="true"]');
      if (selectedOption) {
        setActiveOption(state, state.options.indexOf(selectedOption));
        scrollOptionIntoListbox(state, selectedOption);
      }
    };
    root.close = (focusOnTrigger = true) => closePopover(state, focusOnTrigger);
    root.togglePopover = () => state.trigger.getAttribute('aria-expanded') === 'true' ? root.close() : root.open();

    const handleTriggerKeydown = (event) => handleKeyNavigation(event, root);
    const handleTriggerClick = root.togglePopover;
    const handleListboxMousemove = (event) => {
      const option = event.target.closest('[role="option"]');
      if (option && state.visibleOptions.includes(option)) {
        const index = state.options.indexOf(option);
        if (index !== state.activeIndex) setActiveOption(state, index);
      }
    };
    const handleListboxMouseleave = () => {
      const selectedOption = state.listbox.querySelector('[role="option"][aria-selected="true"]');
      setActiveOption(state, selectedOption ? state.options.indexOf(selectedOption) : -1);
    };
    const handleListboxClick = (event) => {
      const clickedOption = event.target.closest('[role="option"]');
      if (!clickedOption) return;

      const option = state.options.find(opt => opt === clickedOption);
      if (!option) return;

      if (state.isMultiple) {
        toggleMultipleValue(root, option);
        if (state.closeOnSelect) {
          root.close();
        } else {
          setActiveOption(state, state.options.indexOf(option));
          state.trigger.focus();
        }
      } else {
        if (state.placeholder && getValue(option) === '') {
          updateValue(root, null);
        } else if (root.value !== getValue(option)) {
          updateValue(root, option);
        }
        root.close();
      }
    };
    const handleDocumentClick = (event) => {
      if (!root.contains(event.target)) root.close(false);
    };
    const handleDocumentPopover = (event) => {
      if (event.detail.source !== root) root.close(false);
    };

    state.trigger.addEventListener('keydown', handleTriggerKeydown);
    state.trigger.addEventListener('click', handleTriggerClick);
    state.listbox.addEventListener('mousemove', handleListboxMousemove);
    state.listbox.addEventListener('mouseleave', handleListboxMouseleave);
    state.listbox.addEventListener('click', handleListboxClick);
    document.addEventListener('click', handleDocumentClick);
    document.addEventListener('basecoat:popover', handleDocumentPopover);

    root._destroy = () => {
      state.trigger.removeEventListener('keydown', handleTriggerKeydown);
      state.trigger.removeEventListener('click', handleTriggerClick);
      state.listbox.removeEventListener('mousemove', handleListboxMousemove);
      state.listbox.removeEventListener('mouseleave', handleListboxMouseleave);
      state.listbox.removeEventListener('click', handleListboxClick);
      document.removeEventListener('click', handleDocumentClick);
      document.removeEventListener('basecoat:popover', handleDocumentPopover);
      states.delete(root);
      delete root.refresh;
      delete root.open;
      delete root.close;
      delete root.togglePopover;
      delete root.select;
      delete root.selectByValue;
      delete root.deselect;
      delete root.toggle;
      delete root.selectAll;
      delete root.selectNone;
    };

    Object.defineProperty(root, 'value', {
      configurable: true,
      get: () => state.isMultiple ? state.options.filter(option => state.selectedOptions.has(option)).map(getValue) : parseStoredValues(state.input.value, state),
      set: (value) => {
        if (state.isMultiple) {
          const values = Array.isArray(value) ? value : (value != null ? [value] : []);
          updateValue(root, values.map(v => state.options.find(option => getValue(option) === v)).filter(Boolean));
        } else {
          if (value == null || value === '') {
            updateValue(root, null);
            root.close();
            return;
          }
          const option = state.options.find(opt => getValue(opt) === value);
          if (option) {
            updateValue(root, option);
            root.close();
          }
        }
      },
    });

    Object.defineProperty(root, 'selected', {
      configurable: true,
      get: () => {
        if (state.isMultiple) return state.options.filter(option => state.selectedOptions.has(option)).map(toSelected);
        const value = root.value;
        const option = state.options.find(opt => getValue(opt) === value);
        return option ? toSelected(option) : null;
      },
    });

    root.select = (value) => selectValue(root, value);
    root.selectByValue = root.select;
    if (state.isMultiple) {
      root.deselect = (value) => deselectValue(root, value);
      root.toggle = (value) => {
        const option = state.options.find(opt => getValue(opt) === value);
        if (option) toggleMultipleValue(root, option);
      };
      root.selectAll = () => updateValue(root, state.options);
      root.selectNone = () => updateValue(root, []);
    }

    state.popover.setAttribute('aria-hidden', 'true');
    state.trigger.setAttribute('aria-expanded', 'false');
    root.dataset.selectInitialized = 'true';
    root.dispatchEvent(new CustomEvent('basecoat:initialized'));
  };

  if (window.basecoat) {
    window.basecoat.register('select', {
      selector: 'div.select:not([data-select-initialized])',
      init: initSelect,
      refresh: refreshSelect,
    });
  }
})();

(() => {
  const initSidebar = (sidebarComponent) => {
    if (sidebarComponent.dataset.sidebarInitialized && typeof sidebarComponent.toggle === 'function') return;

    const initialOpen = sidebarComponent.dataset.initialOpen !== 'false';
    const initialMobileOpen = sidebarComponent.dataset.initialMobileOpen === 'true';
    const breakpoint = parseInt(sidebarComponent.dataset.breakpoint) || 768;

    let open = breakpoint > 0
      ? (window.innerWidth >= breakpoint ? initialOpen : initialMobileOpen)
      : initialOpen;

    const updateState = () => {
      sidebarComponent.setAttribute('aria-hidden', String(!open));
      if (open) {
        sidebarComponent.removeAttribute('inert');
      } else {
        sidebarComponent.setAttribute('inert', '');
      }
    };

    const setState = (state) => {
      open = Boolean(state);
      updateState();
    };

    sidebarComponent.open = () => setState(true);
    sidebarComponent.close = () => setState(false);
    sidebarComponent.toggle = () => setState(!open);

    const handleClick = (event) => {
      const target = event.target;
      const nav = sidebarComponent.querySelector('nav');
      const isMobile = window.innerWidth < breakpoint;

      if (isMobile && target.closest('a, button') && !target.closest('[data-keep-mobile-sidebar-open]')) {
        if (document.activeElement) document.activeElement.blur();
        sidebarComponent.close();
        return;
      }

      if (target === sidebarComponent || (nav && !nav.contains(target))) {
        if (document.activeElement) document.activeElement.blur();
        sidebarComponent.close();
      }
    };

    sidebarComponent.addEventListener('click', handleClick);

    sidebarComponent._destroy = () => {
      sidebarComponent.removeEventListener('click', handleClick);
      delete sidebarComponent.open;
      delete sidebarComponent.close;
      delete sidebarComponent.toggle;
    };

    updateState();
    sidebarComponent.dataset.sidebarInitialized = 'true';
    sidebarComponent.dispatchEvent(new CustomEvent('basecoat:initialized'));
  };

  if (window.basecoat) {
    window.basecoat.register('sidebar', '.sidebar', initSidebar);
  }
})();

(() => {
  const states = new WeakMap();

  const isDisabled = (tab) => tab.disabled || tab.getAttribute('aria-disabled') === 'true';

  const getElements = (root) => {
    const tablist = root.querySelector('[role="tablist"]');
    const tabs = tablist ? Array.from(tablist.querySelectorAll('[role="tab"]')) : [];
    const panels = tabs.map(tab => document.getElementById(tab.getAttribute('aria-controls'))).filter(Boolean);
    return { tablist, tabs, panels };
  };

  const refreshTabs = (root) => {
    const state = states.get(root);
    if (!state) return;

    Object.assign(state, getElements(root));
    if (!state.tablist) return;

    const selected = state.tabs.find(tab => tab.getAttribute('aria-selected') === 'true' && !isDisabled(tab))
      || state.tabs.find(tab => !isDisabled(tab));
    if (selected) root.select(selected, false);
  };

  const initTabs = (root) => {
    if (root.dataset.tabsInitialized) return;

    const state = {};
    states.set(root, state);
    root.refresh = () => refreshTabs(root);

    const selectTab = (tabToSelect, focus = false) => {
      if (!tabToSelect || isDisabled(tabToSelect)) return;

      state.tabs.forEach((tab) => {
        tab.setAttribute('aria-selected', 'false');
        tab.setAttribute('tabindex', '-1');
        const panel = document.getElementById(tab.getAttribute('aria-controls'));
        if (panel) panel.hidden = true;
      });

      tabToSelect.setAttribute('aria-selected', 'true');
      tabToSelect.setAttribute('tabindex', '0');
      const activePanel = document.getElementById(tabToSelect.getAttribute('aria-controls'));
      if (activePanel) activePanel.hidden = false;
      if (focus) tabToSelect.focus();
    };

    root.select = selectTab;
    refreshTabs(root);
    if (!state.tablist) {
      states.delete(root);
      delete root.refresh;
      delete root.select;
      return;
    }

    const handleClick = (event) => {
      const clickedTab = event.target.closest('[role="tab"]');
      if (clickedTab) root.select(clickedTab);
    };

    const handleKeydown = (event) => {
      const currentTab = event.target;
      if (!state.tabs.includes(currentTab)) return;

      const enabledTabs = state.tabs.filter(tab => !isDisabled(tab));
      const currentIndex = enabledTabs.indexOf(currentTab);
      const orientation = state.tablist.getAttribute('aria-orientation') || 'horizontal';
      if (currentIndex === -1) return;

      let nextTab;
      if (event.key === 'ArrowRight' && orientation === 'horizontal') nextTab = enabledTabs[(currentIndex + 1) % enabledTabs.length];
      if (event.key === 'ArrowLeft' && orientation === 'horizontal') nextTab = enabledTabs[(currentIndex - 1 + enabledTabs.length) % enabledTabs.length];
      if (event.key === 'ArrowDown' && orientation === 'vertical') nextTab = enabledTabs[(currentIndex + 1) % enabledTabs.length];
      if (event.key === 'ArrowUp' && orientation === 'vertical') nextTab = enabledTabs[(currentIndex - 1 + enabledTabs.length) % enabledTabs.length];
      if (event.key === 'Home') nextTab = enabledTabs[0];
      if (event.key === 'End') nextTab = enabledTabs[enabledTabs.length - 1];
      if (!nextTab) return;

      event.preventDefault();
      root.select(nextTab, true);
    };

    state.tablist.addEventListener('click', handleClick);
    state.tablist.addEventListener('keydown', handleKeydown);

    root._destroy = () => {
      state.tablist.removeEventListener('click', handleClick);
      state.tablist.removeEventListener('keydown', handleKeydown);
      states.delete(root);
      delete root.refresh;
      delete root.select;
    };

    root.dataset.tabsInitialized = 'true';
    root.dispatchEvent(new CustomEvent('basecoat:initialized'));
  };

  if (window.basecoat) {
    window.basecoat.register('tabs', {
      selector: '.tabs:not([data-tabs-initialized])',
      init: initTabs,
      refresh: refreshTabs,
    });
  }
})();

(() => {
  let toaster;
  const toasts = new WeakMap();
  let isPaused = false;
  const ICONS = {
    success: '<svg aria-hidden="true" xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><path d="m9 12 2 2 4-4"/></svg>',
    error: '<svg aria-hidden="true" xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><path d="m15 9-6 6"/><path d="m9 9 6 6"/></svg>',
    info: '<svg aria-hidden="true" xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/></svg>',
    warning: '<svg aria-hidden="true" xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/><path d="M12 9v4"/><path d="M12 17h.01"/></svg>'
  };

  function initToaster(toasterElement) {
    if (toasterElement.dataset.toasterInitialized) return;
    toaster = toasterElement;

    const handleClick = (event) => {
      const actionLink = event.target.closest('.toast footer a');
      const actionButton = event.target.closest('.toast footer button');
      if (actionLink || actionButton) {
        closeToast(event.target.closest('.toast'));
      }
    };

    toaster.addEventListener('mouseenter', pauseAllTimeouts);
    toaster.addEventListener('mouseleave', resumeAllTimeouts);
    toaster.addEventListener('click', handleClick);

    toasterElement.toast = (config = {}) => {
      const toastElement = createToast(config);
      toasterElement.appendChild(toastElement);
      initToast(toastElement);
      return toastElement;
    };
    toasterElement.closeAll = () => {
      toasterElement.querySelectorAll('.toast:not([aria-hidden="true"])').forEach(closeToast);
    };

    toaster.querySelectorAll('.toast:not([data-toast-initialized])').forEach(initToast);
    toaster._destroy = () => {
      toaster.removeEventListener('mouseenter', pauseAllTimeouts);
      toaster.removeEventListener('mouseleave', resumeAllTimeouts);
      toaster.removeEventListener('click', handleClick);
      toaster.querySelectorAll('.toast[data-toast-initialized]').forEach(toast => toast._destroy?.());
      delete toaster.toast;
      delete toaster.closeAll;
      if (toaster === toasterElement) toaster = null;
    };
    toaster.dataset.toasterInitialized = 'true';
    toaster.dispatchEvent(new CustomEvent('basecoat:initialized'));
  }

  function initToast(element) {
    if (element.dataset.toastInitialized) return;

    const duration = parseInt(element.dataset.duration);
    const timeoutDuration = duration !== -1
      ? duration || (element.dataset.category === 'error' ? 5000 : 3000)
      : -1;

    const state = {
      remainingTime: timeoutDuration,
      timeoutId: null,
      startTime: null,
    };

    if (timeoutDuration !== -1) {
      if (isPaused) {
        state.timeoutId = null;
      } else {
        state.startTime = Date.now();
        state.timeoutId = setTimeout(() => closeToast(element), timeoutDuration);
      }
    }
    toasts.set(element, state);

    element.close = () => closeToast(element);
    element._destroy = () => {
      clearTimeout(state.timeoutId);
      toasts.delete(element);
      delete element.close;
    };
    element.dataset.toastInitialized = 'true';
  }

  function pauseAllTimeouts() {
    if (isPaused || !toaster) return;

    isPaused = true;

    toaster.querySelectorAll('.toast:not([aria-hidden="true"])').forEach(element => {
      if (!toasts.has(element)) return;

      const state = toasts.get(element);
      if (state.timeoutId) {
        clearTimeout(state.timeoutId);
        state.timeoutId = null;
        state.remainingTime -= Date.now() - state.startTime;
      }
    });
  }

  function resumeAllTimeouts() {
    if (!isPaused || !toaster) return;

    isPaused = false;

    toaster.querySelectorAll('.toast:not([aria-hidden="true"])').forEach(element => {
      if (!toasts.has(element)) return;

      const state = toasts.get(element);
      if (state.remainingTime !== -1 && !state.timeoutId) {
        if (state.remainingTime > 0) {
          state.startTime = Date.now();
          state.timeoutId = setTimeout(() => closeToast(element), state.remainingTime);
        } else {
          closeToast(element);
        }
      }
    });
  }

  function closeToast(element) {
    if (!element || !toasts.has(element)) return;

    const state = toasts.get(element);
    clearTimeout(state.timeoutId);
    toasts.delete(element);

    if (element.contains(document.activeElement)) document.activeElement.blur();
    element.setAttribute('aria-hidden', 'true');
    element.addEventListener('transitionend', () => element.remove(), { once: true });
  }

  function createToast(config) {
    const {
      category = 'info',
      title,
      description,
      action,
      cancel,
      duration,
      icon,
    } = config;

    const iconHtml = icon || (category && ICONS[category]) || '';
    const titleHtml = title ? `<h2>${title}</h2>` : '';
    const descriptionHtml = description ? `<p>${description}</p>` : '';
    const actionHtml = action?.href
      ? `<a href="${action.href}" class="btn" data-toast-action>${action.label}</a>`
      : action?.onclick
        ? `<button type="button" class="btn" data-toast-action onclick="${action.onclick}">${action.label}</button>`
        : '';
    const cancelHtml = cancel
      ? `<button type="button" class="btn h-6 text-xs px-2.5 rounded-sm" data-variant="outline" data-toast-cancel onclick="${cancel?.onclick || ''}">${cancel.label}</button>`
      : '';

    const footerHtml = actionHtml || cancelHtml ? `<footer>${actionHtml}${cancelHtml}</footer>` : '';

    const html = `
      <div
        class="toast"
        role="${category === 'error' ? 'alert' : 'status'}"
        aria-atomic="true"
        ${category ? `data-category="${category}"` : ''}
        ${duration !== undefined ? `data-duration="${duration}"` : ''}
      >
        <div class="toast-content">
          ${iconHtml}
          <section>
            ${titleHtml}
            ${descriptionHtml}
          </section>
          ${footerHtml}
        </div>
      </div>
    `;
    const template = document.createElement('template');
    template.innerHTML = html.trim();
    return template.content.firstChild;
  }

  if (window.basecoat) {
    window.basecoat.register('toaster', '#toaster:not([data-toaster-initialized])', initToaster);
    window.basecoat.register('toast', '.toast:not([data-toast-initialized])', initToast);
  }
})();

