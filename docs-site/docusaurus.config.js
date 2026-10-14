/** @type {import('@docusaurus/types').Config} */
const config = {
  title: 'Vigira Documentation',
  tagline: 'Guides and technical reference for Vigira',
  favicon: 'img/logo.svg',
  url: 'https://deranjer.github.io',
  baseUrl: '/vigara/docs/',
  organizationName: 'deranjer',
  projectName: 'vigara',
  trailingSlash: true,
  onBrokenLinks: 'throw',
  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'warn',
    },
  },
  presets: [['classic', {
    docs: {path: '../docs', routeBasePath: '/', sidebarPath: './sidebars.js'},
    blog: false,
    theme: {customCss: './src/css/custom.css'},
  }]],
  themeConfig: {
    navbar: {
      title: 'Vigira', logo: {alt: 'Vigira', src: 'img/logo.svg'},
      items: [
        {type: 'docSidebar', sidebarId: 'docs', position: 'left', label: 'Documentation'},
        {href: 'https://github.com/deranjer/vigara', label: 'GitHub', position: 'right'},
      ],
    },
    footer: {
      style: 'dark',
      links: [{title: 'Project', items: [{label: 'GitHub repository', href: 'https://github.com/deranjer/vigara'}]}],
      copyright: `Copyright © ${new Date().getFullYear()} Vigira contributors.`,
    },
    prism: {additionalLanguages: ['bash', 'go', 'json', 'yaml']},
  },
};

export default config;
