import { Meta, StoryObj } from "@storybook/react";

import { DEFAULT_GRAVATAR_LINK } from "utilities/constants";

import Avatar from ".";

const meta: Meta<typeof Avatar> = {
  component: Avatar,
  title: "Components/Avatar",
  args: {
    user: { gravatar_url: DEFAULT_GRAVATAR_LINK },
  },
};

export default meta;

type Story = StoryObj<typeof Avatar>;

export const Default: Story = {};

export const Small: Story = {
  args: {
    size: "small",
  },
};
