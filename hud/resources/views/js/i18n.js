'use strict';

/**
 * Internationalization (i18n) module for TSW HUD Project
 * Handles language loading, translation, and persistence
 */

const i18n = (function() {
    // Supported languages with flag codes and names
    const SUPPORTED_LANGUAGES = {
        'en': { flag: 'gb', name: 'English', nativeName: 'English' },
        'en-US': { flag: 'us', name: 'English (US)', nativeName: 'English (US)' },
        'fr': { flag: 'fr', name: 'French', nativeName: 'Français' },
        'de': { flag: 'de', name: 'German', nativeName: 'Deutsch' },
        'it': { flag: 'it', name: 'Italian', nativeName: 'Italiano' },
        'es': { flag: 'es', name: 'Spanish', nativeName: 'Español' },
        'pl': { flag: 'pl', name: 'Polish', nativeName: 'Polski' },
        'ru': { flag: 'ru', name: 'Russian', nativeName: 'Русский' },
        'zh': { flag: 'cn', name: 'Chinese', nativeName: '中文' },
        'ja': { flag: 'jp', name: 'Japanese', nativeName: '日本語' }
    };

    const DEFAULT_LANGUAGE = 'en';
    let currentLanguage = DEFAULT_LANGUAGE;
    let translations = {};
    let isLoaded = false;
    let loadPromise = null;

    /**
     * Get nested property from object using dot notation
     */
    function getNestedValue(obj, path) {
        return path.split('.').reduce((current, key) => {
            return current && current[key] !== undefined ? current[key] : null;
        }, obj);
    }

    /**
     * Load translations for a specific language
     */
    async function loadTranslations(lang) {
        if (!SUPPORTED_LANGUAGES[lang]) {
            console.warn('Unsupported language:', lang, '- falling back to', DEFAULT_LANGUAGE);
            lang = DEFAULT_LANGUAGE;
        }

        try {
            const response = await fetch('/locales/' + lang + '.json');
            if (!response.ok) {
                throw new Error('Failed to load translations for ' + lang);
            }
            translations = await response.json();
            currentLanguage = lang;
            isLoaded = true;
            return true;
        } catch (err) {
            console.error('Error loading translations:', err);
            // Try to load default language if current failed
            if (lang !== DEFAULT_LANGUAGE) {
                return loadTranslations(DEFAULT_LANGUAGE);
            }
            return false;
        }
    }

    /**
     * Get translation for a key
     * Supports nested keys like 'nav.home' and interpolation like 'Hello {name}'
     */
    function t(key, params) {
        if (!isLoaded) {
            return key;
        }

        let text = getNestedValue(translations, key);

        if (text === null || text === undefined) {
            // Return the key itself if translation not found
            console.warn('Translation missing for key:', key);
            return key;
        }

        // Handle interpolation
        if (params && typeof text === 'string') {
            Object.keys(params).forEach(function(param) {
                text = text.replace(new RegExp('\\{' + param + '\\}', 'g'), params[param]);
            });
        }

        return text;
    }

    /**
     * Translate all elements with data-i18n attribute
     */
    function translatePage() {
        // Translate text content
        document.querySelectorAll('[data-i18n]').forEach(function(el) {
            const key = el.getAttribute('data-i18n');
            const translation = t(key);
            if (translation !== key) {
                el.textContent = translation;
            }
        });

        // Translate placeholders
        document.querySelectorAll('[data-i18n-placeholder]').forEach(function(el) {
            const key = el.getAttribute('data-i18n-placeholder');
            const translation = t(key);
            if (translation !== key) {
                el.placeholder = translation;
            }
        });

        // Translate titles
        document.querySelectorAll('[data-i18n-title]').forEach(function(el) {
            const key = el.getAttribute('data-i18n-title');
            const translation = t(key);
            if (translation !== key) {
                el.title = translation;
            }
        });

        // Translate alt text
        document.querySelectorAll('[data-i18n-alt]').forEach(function(el) {
            const key = el.getAttribute('data-i18n-alt');
            const translation = t(key);
            if (translation !== key) {
                el.alt = translation;
            }
        });

        // Update page title if specified
        const titleKey = document.documentElement.getAttribute('data-i18n-title');
        if (titleKey) {
            document.title = t(titleKey);
        }
    }

    /**
     * Set language and reload translations
     */
    async function setLanguage(lang) {
        if (!SUPPORTED_LANGUAGES[lang]) {
            console.warn('Unsupported language:', lang);
            return false;
        }

        // Save to localStorage
        localStorage.setItem('language', lang);

        // Save to server config
        try {
            await fetch('/api/config', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ language: lang })
            });
        } catch (err) {
            console.error('Failed to save language to server:', err);
        }

        // Load new translations
        const success = await loadTranslations(lang);
        if (success) {
            translatePage();
            updateLanguageSelectors();
            // Dispatch event so pages can re-render dynamic content
            document.dispatchEvent(new CustomEvent('languageChanged', {
                detail: { language: currentLanguage }
            }));
        }
        return success;
    }

    /**
     * Get current language
     */
    function getLanguage() {
        return currentLanguage;
    }

    /**
     * Get all supported languages
     */
    function getSupportedLanguages() {
        return SUPPORTED_LANGUAGES;
    }

    /**
     * Update all language selector dropdowns on the page
     */
    function updateLanguageSelectors() {
        // Update nav flag dropdown
        updateNavFlagDropdown();

        // Update settings selector
        document.querySelectorAll('.language-option').forEach(function(opt) {
            opt.classList.remove('active');
            const radio = opt.querySelector('input[type="radio"]');
            if (radio) {
                radio.checked = (radio.value === currentLanguage);
                if (radio.checked) {
                    opt.classList.add('active');
                }
            }
        });
    }

    /**
     * Update the nav flag dropdown to reflect current language (DOM created by navbar.js)
     */
    function updateNavFlagDropdown() {
        const container = document.querySelector('.language-selector-nav');
        if (!container) return;

        const langInfo = SUPPORTED_LANGUAGES[currentLanguage] || SUPPORTED_LANGUAGES[DEFAULT_LANGUAGE];

        // Update current flag display
        const currentFlagEl = container.querySelector('.current-flag');
        if (currentFlagEl) {
            const flagSpan = currentFlagEl.querySelector('.fi');
            if (flagSpan) {
                flagSpan.className = 'fi fi-' + langInfo.flag;
            }
        }

        // Update active state in dropdown
        container.querySelectorAll('.flag-option').forEach(function(opt) {
            opt.classList.remove('active');
            if (opt.dataset.lang === currentLanguage) {
                opt.classList.add('active');
            }
        });
    }

    /**
     * Update the flag icon in nav language selector (legacy support)
     */
    function updateNavFlag() {
        updateNavFlagDropdown();
    }

    /**
     * Initialize i18n system
     */
    async function init() {
        if (loadPromise) {
            return loadPromise;
        }

        loadPromise = (async function() {
            // The desktop shell's language selector is the source of truth — it
            // writes configuration.json.language. Prefer that over the browser's
            // localStorage so served HUD pages follow the app-wide language;
            // localStorage is only a fallback when the server is unreachable.
            let lang = null;
            try {
                const response = await fetch('/api/config');
                const config = await response.json();
                lang = config.language;
            } catch (err) {
                console.error('Failed to load config:', err);
            }
            if (!lang) lang = localStorage.getItem('language');

            // Default to English if nothing found
            lang = lang || DEFAULT_LANGUAGE;
            // Keep localStorage in sync so the in-page flag selector matches.
            try { localStorage.setItem('language', lang); } catch (e) {}

            // Load translations
            await loadTranslations(lang);
            translatePage();
            updateLanguageSelectors();

            return true;
        })();

        return loadPromise;
    }

    /**
     * Create settings language selector HTML
     */
    function createSettingsLanguageSelector() {
        let html = '<div class="language-selector-settings">';

        Object.keys(SUPPORTED_LANGUAGES).forEach(function(code) {
            const lang = SUPPORTED_LANGUAGES[code];
            const isActive = code === currentLanguage ? ' active' : '';
            const isChecked = code === currentLanguage ? ' checked' : '';

            html += '<label class="language-option' + isActive + '" data-lang="' + code + '">';
            html += '<input type="radio" name="language" value="' + code + '"' + isChecked + '>';
            html += '<span class="fi fi-' + lang.flag + '"></span>';
            html += '<span class="language-name">' + lang.nativeName + '</span>';
            html += '<span class="language-name-english">(' + lang.name + ')</span>';
            html += '</label>';
        });

        html += '</div>';

        return html;
    }

    /**
     * Translate elements within a specific container
     */
    function translate(container) {
        if (!container) {
            return translatePage();
        }

        // Translate text content
        container.querySelectorAll('[data-i18n]').forEach(function(el) {
            const key = el.getAttribute('data-i18n');
            const translation = t(key);
            if (translation !== key) {
                el.textContent = translation;
            }
        });

        // Translate placeholders
        container.querySelectorAll('[data-i18n-placeholder]').forEach(function(el) {
            const key = el.getAttribute('data-i18n-placeholder');
            const translation = t(key);
            if (translation !== key) {
                el.placeholder = translation;
            }
        });

        // Translate titles
        container.querySelectorAll('[data-i18n-title]').forEach(function(el) {
            const key = el.getAttribute('data-i18n-title');
            const translation = t(key);
            if (translation !== key) {
                el.title = translation;
            }
        });
    }

    // Public API
    return {
        init: init,
        t: t,
        translate: translate,
        setLanguage: setLanguage,
        getLanguage: getLanguage,
        getSupportedLanguages: getSupportedLanguages,
        translatePage: translatePage,
        createSettingsLanguageSelector: createSettingsLanguageSelector,
        updateLanguageSelectors: updateLanguageSelectors,
        SUPPORTED_LANGUAGES: SUPPORTED_LANGUAGES,
        DEFAULT_LANGUAGE: DEFAULT_LANGUAGE
    };
})();

// Auto-initialize when DOM is ready
if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function() {
        i18n.init();
    });
} else {
    i18n.init();
}
