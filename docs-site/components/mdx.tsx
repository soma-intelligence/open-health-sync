import { Accordion, Accordions } from "fumadocs-ui/components/accordion";
import { File, Files, Folder } from "fumadocs-ui/components/files";
import { Step, Steps } from "fumadocs-ui/components/steps";
import { Tab, Tabs } from "fumadocs-ui/components/tabs";
import { TypeTable } from "fumadocs-ui/components/type-table";
import defaultMdxComponents from "fumadocs-ui/mdx";
import type { MDXComponents } from "mdx/types";
import type { ComponentProps, ComponentType } from "react";
import { docsHref } from "@/lib/shared";
import { Pipeline } from "./pipeline";
import { Shot } from "./shot";
import { Status } from "./status";

/** A link component that sends /documentation/... links to where the guides are served. */
export function docsLink<P extends { href?: string }>(Link: ComponentType<P>) {
  return function DocsLink(props: P) {
    return <Link {...props} href={props.href === undefined ? undefined : docsHref(props.href)} />;
  };
}

const Card = docsLink(defaultMdxComponents.Card as ComponentType<ComponentProps<typeof defaultMdxComponents.Card>>);

export function getMDXComponents(components?: MDXComponents) {
  return {
    ...defaultMdxComponents,
    a: docsLink(defaultMdxComponents.a as ComponentType<ComponentProps<"a">>),
    Card,
    Accordion,
    Accordions,
    File,
    Files,
    Folder,
    Pipeline,
    Shot,
    Status,
    Step,
    Steps,
    Tab,
    Tabs,
    TypeTable,
    ...components,
  } satisfies MDXComponents;
}

export const useMDXComponents = getMDXComponents;

declare global {
  type MDXProvidedComponents = ReturnType<typeof getMDXComponents>;
}
